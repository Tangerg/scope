package s3vectors

import (
	"cmp"
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3vectors"
	s3vdoc "github.com/aws/aws-sdk-go-v2/service/s3vectors/document"
	"github.com/aws/aws-sdk-go-v2/service/s3vectors/types"
	smithyjson "github.com/aws/smithy-go/document/json"

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
	GetIndex(context.Context, *s3vectors.GetIndexInput, ...func(*s3vectors.Options)) (*s3vectors.GetIndexOutput, error)
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

	// DistanceMetric records the metric the index was created with. It maps
	// QueryVectors distances to scores and scores vectors locally when a
	// filter requires exhaustive enumeration. The index is provisioned out of band.
	DistanceMetric DistanceMetric
}

// ErrIncompatibleIndex reports an index that is not the one the store was
// configured for: it was registered with a different distance metric.
var ErrIncompatibleIndex = errors.New("s3vectors: index is incompatible")

// DistanceMetric is the metric registered with the S3 Vectors index, checked
// against the index at construction so native and local ranking use one metric.
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

// distance keeps ranking in the native metric before projection to a bounded
// score can merge nearby values. Inputs were narrowed to finite float32, so
// squared components and their sums fit float64 throughout S3's dimension range.
func (d DistanceMetric) distance(left, right []float64) float64 {
	if d == DistanceEuclidean {
		var distance float64
		for index := range left {
			distance = math.Hypot(distance, left[index]-right[index])
		}
		return distance
	}
	var dot, leftSquared, rightSquared float64
	for index := range left {
		dot += left[index] * right[index]
		leftSquared += left[index] * left[index]
		rightSquared += right[index] * right[index]
	}
	if leftSquared == 0 || rightSquared == 0 {
		return 1
	}
	return 1 - max(-1, min(1, dot/(math.Sqrt(leftSquared)*math.Sqrt(rightSquared))))
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

// NewStore confirms the index agrees with the configured metric during
// construction, which is why it takes a context: a store returned with the
// wrong metric would go on returning scores that are wrong rather than absent,
// and the misconfiguration is at wiring.
func NewStore(ctx context.Context, config StoreConfig) (*Store, error) {
	config.applyDefaults()
	if err := config.Validate(); err != nil {
		return nil, err
	}

	embeddingClient, err := embeddingclient.New(config.EmbeddingModel)
	if err != nil {
		return nil, fmt.Errorf("s3vectors: create embedding client: %w", err)
	}

	index, err := config.Client.GetIndex(ctx, &s3vectors.GetIndexInput{
		VectorBucketName: aws.String(config.VectorBucketName),
		IndexName:        aws.String(config.IndexName),
	})
	if err != nil {
		return nil, fmt.Errorf("s3vectors: describe index %s in bucket %s: %w",
			config.IndexName, config.VectorBucketName, err)
	}
	if err = validateIndexMetric(index.Index, config.IndexName, config.DistanceMetric); err != nil {
		return nil, err
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

// validateIndexMetric refuses a store whose configured metric is not the one
// the index was registered with.
//
// Reading cosine distance as Euclidean, or the reverse, yields plausible
// scores in the wrong scale and MinScore then filters the wrong rows.
//
// Dimensionality is deliberately not compared. This store declares no
// dimension of its own, and a vector of the wrong width is rejected by S3
// Vectors on the first write — that half already fails loudly.
func validateIndexMetric(index *types.Index, name string, want DistanceMetric) error {
	if index == nil {
		return fmt.Errorf("%w: index %s returned no attributes", ErrIncompatibleIndex, name)
	}
	if DistanceMetric(index.DistanceMetric) != want {
		return fmt.Errorf("%w: index %s was created with metric %q, but the store is configured for %q",
			ErrIncompatibleIndex, name, index.DistanceMetric, want)
	}
	return nil
}

// Index embeds documents and PUTs them, splitting each batch at
// [MaxVectorsPerWrite]. Leaving that to the caller's batcher would make a
// documented provider limit their problem, and a larger shard is a request
// certain to be rejected.
func (s *Store) Index(ctx context.Context, request *vectorstore.IndexRequest) (err error) {
	if validateErr := request.Validate(); validateErr != nil {
		return fmt.Errorf("s3vectors.Store.Index: %w", validateErr)
	}
	for index, doc := range request.Documents {
		if doc.Media != nil {
			return fmt.Errorf("s3vectors.Store.Index: %w: documents[%d] contains unsupported media", vectorstore.ErrInvalidDocument, index)
		}
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
			var encoded map[string]any
			var decoder smithyjson.Decoder
			if err := decoder.DecodeJSONInterface(meta, &encoded); err != nil {
				return fmt.Errorf("s3vectors: encode metadata for %s: %w", id, err)
			}

			records = append(records, types.PutInputVector{
				Key:      aws.String(id),
				Data:     &types.VectorDataMemberFloat32{Value: embedding.Float32Vector(vectors[i])},
				Metadata: s3vdoc.NewLazyDocument(encoded),
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

// Search uses QueryVectors without a filter. Filtered search exhaustively
// lists metadata and vector data, evaluates membership with [filter.Match],
// and ranks every match with the index metric before applying TopK. S3's
// native scalar equality also matches array elements and its query API cannot
// restrict results by key, so it cannot express the Core filter contract.
// Filtered search requires s3vectors:ListVectors and s3vectors:GetVectors and
// reads the full index. Concurrent writes are not isolated by this scan.
func (s *Store) Search(ctx context.Context, request *vectorstore.SearchRequest) (response *vectorstore.SearchResponse, err error) {
	var docs []*vectorstore.SearchResult
	if err = request.Validate(); err != nil {
		return nil, fmt.Errorf("s3vectors.Store.Search: %w", err)
	}
	if err = request.Options.RequireMode(vectorstore.SearchModeSemantic); err != nil {
		return nil, fmt.Errorf("s3vectors.Store.Search: %w", err)
	}

	defer func() {
		if err == nil {
			err = response.ValidateFor(request)
		}
	}()

	vector, err := s.embeddingClient.EmbedText(ctx, request.Query)
	if err != nil {
		return nil, fmt.Errorf("s3vectors: embed query: %w", err)
	}
	queryVec := embedding.Float32Vector(vector)

	limit := request.Options.ResultLimit()
	if limit > MaxTopK {
		return nil, fmt.Errorf("s3vectors.Store.Search: TopK %d exceeds the %d results a query can return",
			limit, MaxTopK)
	}
	if request.Options.Filter != nil {
		return s.searchFiltered(ctx, request, queryVec)
	}

	input := &s3vectors.QueryVectorsInput{
		VectorBucketName: aws.String(s.vectorBucketName),
		IndexName:        aws.String(s.indexName),
		QueryVector:      &types.VectorDataMemberFloat32{Value: queryVec},
		TopK:             aws.Int32(int32(limit)),
		ReturnDistance:   true,
		ReturnMetadata:   true,
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
			match, matchErr := s.toMatch(hit, request.Options.MinScore)
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

func (s *Store) searchFiltered(ctx context.Context, req *vectorstore.SearchRequest, query []float32) (*vectorstore.SearchResponse, error) {
	queryVector := make([]float64, len(query))
	for index, value := range query {
		queryVector[index] = float64(value)
		if math.IsNaN(queryVector[index]) || math.IsInf(queryVector[index], 0) {
			return nil, errors.New("s3vectors: query vector is not finite float32")
		}
	}
	type rankedVector struct {
		distance float64
		result   *vectorstore.SearchResult
	}
	var candidates []rankedVector
	err := s.visitMatchingVectors(ctx, req.Options.Filter, true, func(listed types.ListOutputVector, doc *document.Document) error {
		data, ok := listed.Data.(*types.VectorDataMemberFloat32)
		if !ok || data == nil || len(data.Value) == 0 || len(data.Value) != len(queryVector) {
			return fmt.Errorf("s3vectors: vector %s has missing or incompatible float32 data", doc.ID)
		}
		vector := make([]float64, len(data.Value))
		for index, value := range data.Value {
			vector[index] = float64(value)
			if math.IsNaN(vector[index]) || math.IsInf(vector[index], 0) {
				return fmt.Errorf("s3vectors: vector %s has non-finite data", doc.ID)
			}
		}
		distance := s.distanceMetric.distance(queryVector, vector)
		score := s.distanceMetric.score(distance)
		if score >= req.Options.MinScore {
			candidates = append(candidates, rankedVector{distance: distance, result: &vectorstore.SearchResult{Document: doc, Score: score}})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	slices.SortFunc(candidates, func(left, right rankedVector) int {
		if order := cmp.Compare(left.distance, right.distance); order != 0 {
			return order
		}
		return cmp.Compare(left.result.Document.ID, right.result.Document.ID)
	})
	results := make([]*vectorstore.SearchResult, min(len(candidates), req.Options.ResultLimit()))
	for index := range results {
		results[index] = candidates[index].result
	}
	return &vectorstore.SearchResponse{Results: results}, nil
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
func (s *Store) DeleteWhere(ctx context.Context, predicate filter.Predicate) (err error) {
	if predicate == nil {
		return vectorstore.ErrMissingFilter
	}
	if err = predicate.Validate(); err != nil {
		return fmt.Errorf("s3vectors.Store.DeleteWhere: %w", err)
	}

	var keys []string
	err = s.visitMatchingVectors(ctx, predicate, false, func(_ types.ListOutputVector, doc *document.Document) error {
		keys = append(keys, doc.ID)
		return nil
	})
	if err != nil {
		return err
	}
	return s.DeleteIDs(ctx, keys)
}

// visitMatchingVectors owns complete enumeration and Core membership for both
// search and deletion. A short or empty page can still have a continuation.
func (s *Store) visitMatchingVectors(ctx context.Context, expr filter.Predicate, returnData bool, visit func(types.ListOutputVector, *document.Document) error) error {
	const pageSize int32 = 500
	var token *string
	for {
		page, err := s.client.ListVectors(ctx, &s3vectors.ListVectorsInput{
			VectorBucketName: aws.String(s.vectorBucketName),
			IndexName:        aws.String(s.indexName),
			MaxResults:       aws.Int32(pageSize),
			NextToken:        token,
			ReturnMetadata:   true,
			ReturnData:       returnData,
		})
		if err != nil {
			return fmt.Errorf("s3vectors: list vectors: %w", err)
		}
		if page == nil {
			return errors.New("s3vectors: ListVectors returned no response")
		}
		for index := range page.Vectors {
			listed := &page.Vectors[index]
			if listed.Key == nil || *listed.Key == "" {
				return fmt.Errorf("s3vectors: listed vector[%d] is missing key", index)
			}
			text, values, err := decodeVectorMetadata(*listed.Key, listed.Metadata)
			if err != nil {
				return err
			}
			matched, err := filter.Match(expr, values)
			if err != nil {
				return fmt.Errorf("s3vectors: evaluate filter for %s: %w", *listed.Key, err)
			}
			if matched {
				documentMetadata, err := metadata.FromValues(values)
				if err != nil {
					return fmt.Errorf("s3vectors: convert metadata for %s: %w", *listed.Key, err)
				}
				if err := visit(*listed, &document.Document{ID: *listed.Key, Text: text, Metadata: documentMetadata}); err != nil {
					return err
				}
			}
		}
		if page.NextToken == nil || *page.NextToken == "" {
			return nil
		}
		if token != nil && *page.NextToken == *token {
			return errors.New("s3vectors: ListVectors repeated its continuation token")
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
	var encodedValues metadata.Map
	if decodeErr := jsonv2.Unmarshal(encodedDocument, &encodedValues); decodeErr != nil {
		return "", nil, fmt.Errorf("s3vectors: decode metadata for %s: %w", key, decodeErr)
	}
	values, err := encodedValues.Values()
	if err != nil {
		return "", nil, err
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
