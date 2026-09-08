package s3vectors

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3vectors"
	s3vdoc "github.com/aws/aws-sdk-go-v2/service/s3vectors/document"
	"github.com/aws/aws-sdk-go-v2/service/s3vectors/types"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/embeddingclient"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

// Documented S3 Vectors operation limits. A request past any of them is
// rejected by the service, so the store either splits the work or refuses
// locally instead of sending one that cannot succeed.
const (
	// MaxTopK is the largest number of results one query may rank.
	MaxTopK = 10_000

	// MaxResultsPerQueryPage is the largest number of hits one QueryVectors
	// response carries; the rest arrive under a continuation token.
	MaxResultsPerQueryPage = 100

	// MaxVectorsPerWrite is the largest number of vectors one PutVectors or
	// DeleteVectors call may carry.
	MaxVectorsPerWrite = 500
)

// Provider is the stable backend name for host-side attribution.
const Provider = "S3Vectors"

const (
	contentMetaKey = "scope_content"
)

// VectorClient is the narrow S3 Vectors surface the store depends on. It is
// declared here rather than accepting the whole SDK client so the store's
// requirements stay visible and a caller can supply a decorated or recorded
// implementation. *s3vectors.Client satisfies it.
type VectorClient interface {
	PutVectors(context.Context, *s3vectors.PutVectorsInput, ...func(*s3vectors.Options)) (*s3vectors.PutVectorsOutput, error)
	QueryVectors(context.Context, *s3vectors.QueryVectorsInput, ...func(*s3vectors.Options)) (*s3vectors.QueryVectorsOutput, error)
	ListVectors(context.Context, *s3vectors.ListVectorsInput, ...func(*s3vectors.Options)) (*s3vectors.ListVectorsOutput, error)
	DeleteVectors(context.Context, *s3vectors.DeleteVectorsInput, ...func(*s3vectors.Options)) (*s3vectors.DeleteVectorsOutput, error)
}

// StoreConfig contains configuration options for the AWS S3 Vectors
// vector store.
type StoreConfig struct {
	// Client is the S3 Vectors API surface, normally an *s3vectors.Client.
	// Required.
	Client VectorClient

	// VectorBucketName names the S3 Vectors bucket. Required.
	VectorBucketName string

	// IndexName names the vector index inside the bucket. Required.
	IndexName string

	// EmbeddingModel produces vectors for the documents. Required.
	EmbeddingModel embedding.Model

	// DocumentBatcher batches documents before upload. Required.
	DocumentBatcher vectorstore.Batcher

	// DistanceMetric records the metric the index was created with —
	// the store uses this only to map the raw distance returned by
	// QueryVectors into a `higher = more similar` [0, 1] score. The
	// actual metric is set on the index out of band.
	DistanceMetric DistanceMetric
}

// DistanceMetric mirrors the metric registered with the S3 Vectors
// index. The store doesn't enforce consistency — picking the wrong
// value here just produces miscalibrated scores.
type DistanceMetric string

// The metric is a closed vocabulary because score direction and threshold
// semantics depend on it: the same raw number means "near" under one metric and
// "far" under another, so an unrecognized value must be rejected rather than
// guessed.
const (
	DistanceCosine    DistanceMetric = "cosine"
	DistanceEuclidean DistanceMetric = "euclidean"
)

func (d DistanceMetric) Valid() bool {
	return d == DistanceCosine || d == DistanceEuclidean
}

func (d DistanceMetric) String() string { return string(d) }

func (d DistanceMetric) score(distance float64) vectorstore.Score {
	switch d {
	case DistanceEuclidean:
		return vectorstore.ScoreFromDistance(distance)
	case DistanceCosine:
		fallthrough
	default:
		return vectorstore.ScoreFromCosineDistance(distance)
	}
}

func (s StoreConfig) Validate() error {
	s.applyDefaults()
	if lo.IsNil(s.Client) {
		return errors.New("s3vectors: Client is required")
	}
	if s.VectorBucketName == "" {
		return errors.New("s3vectors: VectorBucketName is required")
	}
	if s.IndexName == "" {
		return errors.New("s3vectors: IndexName is required")
	}
	if lo.IsNil(s.EmbeddingModel) {
		return errors.New("s3vectors: EmbeddingModel is required")
	}
	if lo.IsNil(s.DocumentBatcher) {
		return errors.New("s3vectors: DocumentBatcher is required")
	}
	if !s.DistanceMetric.Valid() {
		return fmt.Errorf("s3vectors: unsupported DistanceMetric %q", s.DistanceMetric)
	}
	return nil
}

// applyDefaults fills zero fields with documented defaults.
func (s *StoreConfig) applyDefaults() {
	s.DistanceMetric = cmp.Or(s.DistanceMetric, DistanceCosine)
}

var (
	_ vectorstore.Indexer       = (*Store)(nil)
	_ vectorstore.Searcher      = (*Store)(nil)
	_ vectorstore.FilterDeleter = (*Store)(nil)
	_ vectorstore.IDDeleter     = (*Store)(nil)
)

// Store implements vector-store capabilities with Amazon S3 Vectors.
type Store struct {
	client           VectorClient
	vectorBucketName string
	indexName        string
	embeddingClient  embeddingclient.Client
	documentBatcher  vectorstore.Batcher
	distanceMetric   DistanceMetric
}

// NewStore needs no context because construction performs no I/O; the vector
// bucket is provisioned outside this package, so the store only validates
// configuration and assembles state.
func NewStore(config StoreConfig) (*Store, error) {
	config.applyDefaults()
	if err := config.Validate(); err != nil {
		return nil, err
	}

	embeddingClient, err := embeddingclient.New(config.EmbeddingModel)
	if err != nil {
		return nil, fmt.Errorf("s3vectors: create embedding client: %w", err)
	}

	return &Store{
		client:           config.Client,
		vectorBucketName: config.VectorBucketName,
		indexName:        config.IndexName,
		embeddingClient:  embeddingClient,
		documentBatcher:  config.DocumentBatcher,
		distanceMetric:   config.DistanceMetric,
	}, nil
}

// Index embeds documents and PUTs them, splitting each batch at
// [MaxVectorsPerWrite]. Leaving that to the caller's batcher would make a
// documented provider limit their problem, and a larger shard is a request
// certain to be rejected.
func (s *Store) Index(ctx context.Context, request *vectorstore.IndexRequest) (err error) {
	if validateErr := request.Validate(); validateErr != nil {
		return fmt.Errorf("s3vectors.Store.Index: %w", validateErr)
	}

	var batches []*vectorstore.IndexRequest
	batches, err = request.Batch(ctx, s.documentBatcher)
	if err != nil {
		return fmt.Errorf("s3vectors: batch documents: %w", err)
	}

	for _, batch := range batches {
		docs := batch.Documents
		texts, err := batch.Texts()
		if err != nil {
			return fmt.Errorf("vectorstore: project document text: %w", err)
		}
		vectors, err := s.embeddingClient.EmbedTexts(ctx, texts)
		if err != nil {
			return fmt.Errorf("s3vectors: embed documents: %w", err)
		}

		records := make([]types.PutInputVector, 0, len(docs))
		for i, doc := range docs {
			id := doc.ID
			metadataValues, err := doc.Metadata.Values()
			if err != nil {
				return fmt.Errorf("s3vectors: decode metadata for %s: %w", id, err)
			}
			if _, reserved := metadataValues[contentMetaKey]; reserved {
				return fmt.Errorf("s3vectors: document %s metadata uses reserved key %q", id, contentMetaKey)
			}
			meta := make(map[string]any, len(metadataValues)+1)
			maps.Copy(meta, metadataValues)
			// Stash the document text in metadata so retrieval can
			// surface it — S3 Vectors itself only stores vector + key
			// + metadata.
			meta[contentMetaKey] = doc.Text

			records = append(records, types.PutInputVector{
				Key:      aws.String(id),
				Data:     &types.VectorDataMemberFloat32{Value: embedding.Float32Vector(vectors[i])},
				Metadata: s3vdoc.NewLazyDocument(meta),
			})
		}

		for chunk := range slices.Chunk(records, MaxVectorsPerWrite) {
			if _, err := s.client.PutVectors(ctx, &s3vectors.PutVectorsInput{
				VectorBucketName: aws.String(s.vectorBucketName),
				IndexName:        aws.String(s.indexName),
				Vectors:          chunk,
			}); err != nil {
				return fmt.Errorf("s3vectors: PutVectors: %w", err)
			}
		}
	}
	return nil
}

// Search runs QueryVectors with the configured filter.
func (s *Store) Search(ctx context.Context, req *vectorstore.SearchRequest) (response *vectorstore.SearchResponse, err error) {
	var docs []*vectorstore.SearchResult
	if err = req.Validate(); err != nil {
		return nil, fmt.Errorf("s3vectors.Store.Search: %w", err)
	}
	if err = req.Options.RequireMode(vectorstore.SearchModeSemantic); err != nil {
		return nil, fmt.Errorf("s3vectors.Store.Search: %w", err)
	}

	defer func() {
		if err == nil {
			err = response.ValidateFor(req)
		}
	}()

	vector, err := s.embeddingClient.EmbedText(ctx, req.Query)
	if err != nil {
		return nil, fmt.Errorf("s3vectors: embed query: %w", err)
	}
	queryVec := embedding.Float32Vector(vector)

	limit := req.Options.ResultLimit()
	if limit > MaxTopK {
		return nil, fmt.Errorf("s3vectors.Store.Search: TopK %d exceeds the %d results a query can return",
			limit, MaxTopK)
	}

	input := &s3vectors.QueryVectorsInput{
		VectorBucketName: aws.String(s.vectorBucketName),
		IndexName:        aws.String(s.indexName),
		QueryVector:      &types.VectorDataMemberFloat32{Value: queryVec},
		TopK:             aws.Int32(int32(limit)),
		ReturnDistance:   true,
		ReturnMetadata:   true,
	}

	if req.Options.Filter != nil {
		filterDoc, filterErr := s.buildFilter(req.Options.Filter)
		if filterErr != nil {
			return nil, filterErr
		}
		if filterDoc != nil {
			input.Filter = s3vdoc.NewLazyDocument(filterDoc)
		}
	}

	// A QueryVectors response carries at most MaxResultsPerQueryPage hits and
	// a continuation token for the rest, so one call answers a TopK above that
	// only in part. Only an empty token establishes that the ranked run is
	// complete.
	docs = make([]*vectorstore.SearchResult, 0, limit)
	for {
		resp, queryErr := s.client.QueryVectors(ctx, input)
		if queryErr != nil {
			return nil, fmt.Errorf("s3vectors: QueryVectors: %w", queryErr)
		}
		for _, hit := range resp.Vectors {
			match, matchErr := s.toMatch(hit, req.Options.MinScore)
			if matchErr != nil {
				return nil, matchErr
			}
			if match != nil {
				docs = append(docs, match)
			}
		}
		if resp.NextToken == nil || *resp.NextToken == "" {
			return &vectorstore.SearchResponse{Results: docs}, nil
		}
		if len(resp.Vectors) == 0 {
			return nil, errors.New("s3vectors: QueryVectors returned an empty page with a continuation token")
		}
		if len(docs) >= limit {
			return &vectorstore.SearchResponse{Results: docs[:limit]}, nil
		}
		input.NextToken = resp.NextToken
	}
}

// DeleteWhere removes every document matching expr. S3 Vectors has no
// filter-based deletion, and QueryVectors is an approximate nearest-neighbor
// search that answers with up to topK candidates rather than every match, so it
// cannot enumerate a filter exhaustively. The store therefore lists the index
// with ListVectors — exhaustive and key-paginated — and decides membership with
// [filter.Match], the same evaluation the in-memory store uses. Listing
// completes before anything is deleted so pagination never observes its own
// mutations. Requires s3vectors:GetVectors alongside s3vectors:ListVectors,
// because membership needs each vector's metadata. Implements
// [vectorstore.FilterDeleter].
func (s *Store) DeleteWhere(ctx context.Context, expr filter.Predicate) (err error) {
	if expr == nil {
		return vectorstore.ErrMissingFilter
	}
	if err = expr.Validate(); err != nil {
		return fmt.Errorf("s3vectors.Store.DeleteWhere: %w", err)
	}

	keys, err := s.matchingKeys(ctx, expr)
	if err != nil {
		return err
	}
	return s.DeleteIDs(ctx, keys)
}

// matchingKeys walks the whole index. Only a nil continuation token establishes
// that the listing is complete: a page can come back short, or even empty,
// while further pages remain.
func (s *Store) matchingKeys(ctx context.Context, expr filter.Predicate) ([]string, error) {
	const pageSize int32 = 500
	var keys []string
	var token *string
	for {
		page, err := s.client.ListVectors(ctx, &s3vectors.ListVectorsInput{
			VectorBucketName: aws.String(s.vectorBucketName),
			IndexName:        aws.String(s.indexName),
			MaxResults:       aws.Int32(pageSize),
			NextToken:        token,
			ReturnMetadata:   true,
		})
		if err != nil {
			return nil, fmt.Errorf("s3vectors: list vectors: %w", err)
		}
		for index := range page.Vectors {
			listed := &page.Vectors[index]
			if listed.Key == nil || *listed.Key == "" {
				return nil, fmt.Errorf("s3vectors: listed vector[%d] is missing key", index)
			}
			_, values, err := decodeVectorMetadata(*listed.Key, listed.Metadata)
			if err != nil {
				return nil, err
			}
			matched, err := filter.Match(expr, values)
			if err != nil {
				return nil, fmt.Errorf("s3vectors: evaluate filter for %s: %w", *listed.Key, err)
			}
			if matched {
				keys = append(keys, *listed.Key)
			}
		}
		if page.NextToken == nil || *page.NextToken == "" {
			return keys, nil
		}
		token = page.NextToken
	}
}

// DeleteIDs removes vectors by key. An empty slice is a no-op; unknown keys are
// ignored by S3 Vectors.
func (s *Store) DeleteIDs(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	for index, id := range ids {
		if id == "" {
			return fmt.Errorf("s3vectors: delete id[%d] must not be empty", index)
		}
	}
	for chunk := range slices.Chunk(ids, MaxVectorsPerWrite) {
		if _, err := s.client.DeleteVectors(ctx, &s3vectors.DeleteVectorsInput{
			VectorBucketName: aws.String(s.vectorBucketName),
			IndexName:        aws.String(s.indexName),
			Keys:             chunk,
		}); err != nil {
			return fmt.Errorf("s3vectors: DeleteVectors: %w", err)
		}
	}
	return nil
}

func (s *Store) buildFilter(expr filter.Predicate) (map[string]any, error) {
	if expr == nil {
		return nil, nil
	}
	v := newVisitor()
	if err := expr.Accept(v); err != nil {
		return nil, fmt.Errorf("s3vectors: convert filter: %w", err)
	}
	return v.snapshot(), nil
}

func (s *Store) toMatch(hit types.QueryOutputVector, minScore vectorstore.Score) (*vectorstore.SearchResult, error) {
	if hit.Key == nil || *hit.Key == "" {
		return nil, errors.New("s3vectors: query result is missing key")
	}
	if hit.Distance == nil {
		return nil, errors.New("s3vectors: query result is missing distance")
	}
	doc := &document.Document{ID: *hit.Key}
	score := s.distanceMetric.score(float64(*hit.Distance))
	if score < minScore {
		return nil, nil
	}

	text, values, err := decodeVectorMetadata(doc.ID, hit.Metadata)
	if err != nil {
		return nil, err
	}
	documentMetadata, err := metadata.FromValues(values)
	if err != nil {
		return nil, fmt.Errorf("s3vectors: convert metadata for %s: %w", doc.ID, err)
	}
	doc.Text = text
	doc.Metadata = documentMetadata
	return &vectorstore.SearchResult{Document: doc, Score: score}, nil
}

// decodeVectorMetadata is the one decode of an S3 Vectors metadata document.
// Numbers stay json.Number so filter evaluation and stored metadata both
// compare them exactly instead of through a lossy float64. The document text
// travels in metadata because S3 Vectors stores only key, vector, and metadata.
func decodeVectorMetadata(key string, raw s3vdoc.Interface) (string, map[string]any, error) {
	if raw == nil {
		return "", nil, fmt.Errorf("s3vectors: vector %s is missing metadata", key)
	}
	encodedDocument, err := raw.MarshalSmithyDocument()
	if err != nil {
		return "", nil, fmt.Errorf("s3vectors: encode metadata document for %s: %w", key, err)
	}
	var values map[string]any
	decoder := json.NewDecoder(bytes.NewReader(encodedDocument))
	decoder.UseNumber()
	if decodeErr := decoder.Decode(&values); decodeErr != nil {
		return "", nil, fmt.Errorf("s3vectors: decode metadata for %s: %w", key, decodeErr)
	}
	rawText, present := values[contentMetaKey]
	if !present {
		return "", nil, fmt.Errorf("s3vectors: vector %s metadata is missing %q", key, contentMetaKey)
	}
	text, ok := rawText.(string)
	if !ok || text == "" {
		return "", nil, fmt.Errorf("s3vectors: vector %s metadata %q must be a non-empty string, got %T",
			key, contentMetaKey, rawText)
	}
	delete(values, contentMetaKey)
	return text, values, nil
}

func (s *Store) Close() error { return nil }
