package s3vectors

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"

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

// Provider is the stable backend name for host-side attribution.
const Provider = "S3Vectors"

const (
	contentMetaKey = "scope_content"
)

// StoreConfig contains configuration options for the AWS S3 Vectors
// vector store.
type StoreConfig struct {
	// Client is the s3vectors client. Required.
	Client *s3vectors.Client

	// VectorBucketName names the S3 Vectors bucket. Required.
	VectorBucketName string

	// IndexName names the vector index inside the bucket. Required.
	IndexName string

	// EmbeddingModel produces vectors for the documents. Required.
	EmbeddingModel embedding.Model

	// DocumentBatcher batches documents before upload. Required.
	DocumentBatcher vectorstore.Batcher

	// Dimensions stays explicit because filter deletion needs a placeholder
	// vector and must not issue a hidden, billable embedding request.
	Dimensions int

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
	if s.Client == nil {
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
	if s.Dimensions <= 0 {
		return errors.New("s3vectors: Dimensions must be > 0")
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
	client           *s3vectors.Client
	vectorBucketName string
	indexName        string
	embeddingClient  embeddingclient.Client
	documentBatcher  vectorstore.Batcher
	distanceMetric   DistanceMetric
	dimensions       int
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
		dimensions:       config.Dimensions,
	}, nil
}

// Index embeds documents and PUTs them. S3 Vectors caps each
// PutVectors batch at 500 vectors, so the document batcher should
// produce shards smaller than that.
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

		if _, err := s.client.PutVectors(ctx, &s3vectors.PutVectorsInput{
			VectorBucketName: aws.String(s.vectorBucketName),
			IndexName:        aws.String(s.indexName),
			Vectors:          records,
		}); err != nil {
			return fmt.Errorf("s3vectors: PutVectors: %w", err)
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

	input := &s3vectors.QueryVectorsInput{
		VectorBucketName: aws.String(s.vectorBucketName),
		IndexName:        aws.String(s.indexName),
		QueryVector:      &types.VectorDataMemberFloat32{Value: queryVec},
		TopK:             aws.Int32(int32(req.Options.ResultLimit())),
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

	resp, err := s.client.QueryVectors(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("s3vectors: QueryVectors: %w", err)
	}

	docs = make([]*vectorstore.SearchResult, 0, len(resp.Vectors))
	for _, hit := range resp.Vectors {
		match, err := s.toMatch(hit, req.Options.MinScore)
		if err != nil {
			return nil, err
		}
		if match != nil {
			docs = append(docs, match)
		}
	}
	return &vectorstore.SearchResponse{Results: docs}, nil
}

// Delete enumerates ids that match the filter via QueryVectors (S3
// Vectors has no filter-based DeleteVectors) and then issues a
// DeleteVectors call.

func (s *Store) DeleteWhere(ctx context.Context, expr filter.Predicate) (err error) {
	if expr == nil {
		return vectorstore.ErrMissingFilter
	}
	if err = expr.Validate(); err != nil {
		return fmt.Errorf("s3vectors.Store.DeleteWhere: %w", err)
	}

	filterDoc, err := s.buildFilter(expr)
	if err != nil {
		return err
	}
	if filterDoc == nil {
		return errors.New("s3vectors: refusing to delete on empty filter")
	}

	// Use a placeholder embedding to drive the filter scan — the
	// vector itself doesn't matter when the distance is discarded.
	probe := make([]float32, s.dimensions)
	const pageSize int32 = 1000
	for {
		resp, err := s.client.QueryVectors(ctx, &s3vectors.QueryVectorsInput{
			VectorBucketName: aws.String(s.vectorBucketName),
			IndexName:        aws.String(s.indexName),
			QueryVector:      &types.VectorDataMemberFloat32{Value: probe},
			TopK:             aws.Int32(pageSize),
			Filter:           s3vdoc.NewLazyDocument(filterDoc),
		})
		if err != nil {
			return fmt.Errorf("s3vectors: enumerate ids: %w", err)
		}
		if len(resp.Vectors) == 0 {
			return nil
		}
		keys, err := queryVectorKeys(resp.Vectors)
		if err != nil {
			return err
		}
		if err := s.DeleteIDs(ctx, keys); err != nil {
			return err
		}
		if int32(len(resp.Vectors)) < pageSize {
			return nil
		}
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
	if _, err := s.client.DeleteVectors(ctx, &s3vectors.DeleteVectorsInput{
		VectorBucketName: aws.String(s.vectorBucketName),
		IndexName:        aws.String(s.indexName),
		Keys:             ids,
	}); err != nil {
		return fmt.Errorf("s3vectors: DeleteVectors: %w", err)
	}
	return nil
}

func queryVectorKeys(vectors []types.QueryOutputVector) ([]string, error) {
	keys := make([]string, len(vectors))
	for index := range vectors {
		if vectors[index].Key == nil || *vectors[index].Key == "" {
			return nil, fmt.Errorf("s3vectors: query result[%d] is missing key", index)
		}
		keys[index] = *vectors[index].Key
	}
	return keys, nil
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

	text, documentMetadata, err := decodeDocumentMetadata(hit.Metadata)
	if err != nil {
		return nil, err
	}
	doc.Text = text
	doc.Metadata = documentMetadata
	return &vectorstore.SearchResult{Document: doc, Score: score}, nil
}

func decodeDocumentMetadata(raw s3vdoc.Interface) (string, metadata.Map, error) {
	if raw == nil {
		return "", nil, errors.New("s3vectors: query result is missing metadata")
	}
	encodedDocument, err := raw.MarshalSmithyDocument()
	if err != nil {
		return "", nil, fmt.Errorf("s3vectors: encode metadata document: %w", err)
	}
	var values map[string]any
	decoder := json.NewDecoder(bytes.NewReader(encodedDocument))
	decoder.UseNumber()
	if decodeErr := decoder.Decode(&values); decodeErr != nil {
		return "", nil, fmt.Errorf("s3vectors: decode metadata: %w", decodeErr)
	}
	rawText, present := values[contentMetaKey]
	if !present {
		return "", nil, fmt.Errorf("s3vectors: query result metadata is missing %q", contentMetaKey)
	}
	text, ok := rawText.(string)
	if !ok || text == "" {
		return "", nil, fmt.Errorf("s3vectors: query result metadata %q must be a non-empty string, got %T", contentMetaKey, rawText)
	}
	delete(values, contentMetaKey)
	encoded, err := metadata.FromValues(values)
	if err != nil {
		return "", nil, fmt.Errorf("s3vectors: convert metadata: %w", err)
	}
	return text, encoded, nil
}

func (s *Store) Close() error { return nil }
