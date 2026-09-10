package milvus

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/milvus-io/milvus/client/v2/column"
	"github.com/milvus-io/milvus/client/v2/entity"
	"github.com/milvus-io/milvus/client/v2/index"
	"github.com/milvus-io/milvus/client/v2/milvusclient"
	"google.golang.org/grpc"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/embeddingclient"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

// Provider is the stable backend name for host-side attribution.
const (
	Provider = "Milvus"
)

const (
	fieldID      = "id"
	fieldVector  = "vector"
	fieldContent = "content"
	fieldMeta    = "metadata"

	maxIDLength      = 36
	maxContentLength = 65535
)

// StoreConfig contains configuration options for Milvus vector store.
type StoreConfig struct {
	// Client is the Milvus client instance.
	// Required: must be provided, otherwise initialization will fail.
	Client *milvusclient.Client

	// CollectionName is the name of the Milvus collection.
	// Required: must be a non-empty string.
	CollectionName string

	// InitializeSchema indicates whether to automatically create the collection
	// and its vector index if they do not exist.
	// Optional: defaults to false.
	InitializeSchema bool

	// EmbeddingModel is the model used to generate vector embeddings from text.
	// Required: must be provided.
	EmbeddingModel embedding.Model

	// DocumentBatcher is responsible for batching documents before insertion.
	// Required: must be provided.
	DocumentBatcher vectorstore.Batcher

	// Dimensions stays explicit because creating a collection must not trigger
	// a hidden, billable embedding request.
	Dimensions int

	// MetricType is the similarity metric used when creating the vector index.
	// Optional: defaults to entity.COSINE.
	MetricType entity.MetricType
}

func (s StoreConfig) Validate() error {
	s.applyDefaults()
	if s.Client == nil {
		return ErrMissingClient
	}
	if s.CollectionName == "" {
		return ErrMissingCollectionName
	}
	if lo.IsNil(s.EmbeddingModel) {
		return ErrMissingEmbeddingModel
	}
	if lo.IsNil(s.DocumentBatcher) {
		return ErrMissingDocumentBatcher
	}
	if s.InitializeSchema && s.Dimensions <= 0 {
		return fmt.Errorf("milvus: Dimensions must be > 0 when InitializeSchema is enabled")
	}
	switch s.MetricType {
	case entity.COSINE, entity.L2, entity.IP:
	default:
		return fmt.Errorf("milvus: unsupported MetricType %q for a float-vector collection", s.MetricType)
	}
	return nil
}

// applyDefaults fills zero fields. MetricType defaults to [entity.COSINE].
func (s *StoreConfig) applyDefaults() {
	if s.MetricType == "" {
		s.MetricType = entity.COSINE
	}
}

var (
	_ vectorstore.Indexer       = (*Store)(nil)
	_ vectorstore.Searcher      = (*Store)(nil)
	_ vectorstore.FilterDeleter = (*Store)(nil)
	_ vectorstore.IDDeleter     = (*Store)(nil)
)

// collectionClient is the narrow Milvus surface the store depends on. It stays
// unexported because the store receives an already-connected client; it exists
// so the store's requirements are visible and its acknowledgment handling is
// checkable. *milvusclient.Client satisfies it.
type collectionClient interface {
	HasCollection(context.Context, milvusclient.HasCollectionOption, ...grpc.CallOption) (bool, error)
	CreateCollection(context.Context, milvusclient.CreateCollectionOption, ...grpc.CallOption) error
	CreateIndex(context.Context, milvusclient.CreateIndexOption, ...grpc.CallOption) (*milvusclient.CreateIndexTask, error)
	LoadCollection(context.Context, milvusclient.LoadCollectionOption, ...grpc.CallOption) (milvusclient.LoadTask, error)
	Upsert(context.Context, milvusclient.UpsertOption, ...grpc.CallOption) (milvusclient.UpsertResult, error)
	Search(context.Context, milvusclient.SearchOption, ...grpc.CallOption) ([]milvusclient.ResultSet, error)
	Delete(context.Context, milvusclient.DeleteOption, ...grpc.CallOption) (milvusclient.DeleteResult, error)
	Close(context.Context) error
}

// Store implements [vectorstore.Store] against a Milvus collection. Milvus
// requires a loaded collection before search, which is why the collection is
// resolved once at construction rather than per query.
type Store struct {
	client           collectionClient
	embeddingClient  embeddingclient.Client
	documentBatcher  vectorstore.Batcher
	collectionName   string
	metricType       entity.MetricType
	dimensions       int
	initializeSchema bool
}

// NewStore performs schema setup during construction, which is why it takes a
// context: a store returned before its collection exists would fail on the
// first index rather than at wiring, where the misconfiguration actually is.
func NewStore(ctx context.Context, config StoreConfig) (*Store, error) {
	config.applyDefaults()
	if err := config.Validate(); err != nil {
		return nil, err
	}

	embeddingClient, err := embeddingclient.New(config.EmbeddingModel)
	if err != nil {
		return nil, fmt.Errorf("milvus: create embedding client: %w", err)
	}

	store := &Store{
		client:           config.Client,
		embeddingClient:  embeddingClient,
		documentBatcher:  config.DocumentBatcher,
		collectionName:   config.CollectionName,
		metricType:       config.MetricType,
		dimensions:       config.Dimensions,
		initializeSchema: config.InitializeSchema,
	}

	if err = store.initialize(ctx); err != nil {
		return nil, fmt.Errorf("milvus: initialize vector store: %w", err)
	}

	return store, nil
}

func (s *Store) createSchema(dim int64) *entity.Schema {
	return entity.NewSchema().
		WithField(entity.NewField().
			WithName(fieldID).
			WithDataType(entity.FieldTypeVarChar).
			WithMaxLength(maxIDLength).
			WithIsPrimaryKey(true)).
		WithField(entity.NewField().
			WithName(fieldVector).
			WithDataType(entity.FieldTypeFloatVector).
			WithDim(dim)).
		WithField(entity.NewField().
			WithName(fieldContent).
			WithDataType(entity.FieldTypeVarChar).
			WithMaxLength(maxContentLength)).
		WithField(entity.NewField().
			WithName(fieldMeta).
			WithDataType(entity.FieldTypeJSON))
}

func (s *Store) initialize(ctx context.Context) error {
	if !s.initializeSchema {
		return nil
	}

	exists, err := s.client.HasCollection(ctx, milvusclient.NewHasCollectionOption(s.collectionName))
	if err != nil {
		return fmt.Errorf("milvus: check collection existence: %w", err)
	}

	if !exists {
		schema := s.createSchema(int64(s.dimensions))
		if err = s.client.CreateCollection(ctx, milvusclient.NewCreateCollectionOption(s.collectionName, schema)); err != nil {
			return fmt.Errorf("milvus: create collection %s: %w", s.collectionName, err)
		}

		idx := index.NewAutoIndex(s.metricType)
		indexTask, createErr := s.client.CreateIndex(ctx, milvusclient.NewCreateIndexOption(s.collectionName, fieldVector, idx))
		if createErr != nil {
			return fmt.Errorf("milvus: create index on collection %s: %w", s.collectionName, createErr)
		}
		if err = indexTask.Await(ctx); err != nil {
			return fmt.Errorf("milvus: await index creation on collection %s: %w", s.collectionName, err)
		}
	}

	loadTask, err := s.client.LoadCollection(ctx, milvusclient.NewLoadCollectionOption(s.collectionName))
	if err != nil {
		return fmt.Errorf("milvus: load collection %s: %w", s.collectionName, err)
	}
	if err = loadTask.Await(ctx); err != nil {
		return fmt.Errorf("milvus: await collection load %s: %w", s.collectionName, err)
	}

	return nil
}

func (s *Store) buildInsertColumns(docs []*document.Document, vectors [][]float64) ([]column.Column, error) {
	n := len(docs)
	ids := make([]string, n)
	vecs := make([][]float32, n)
	contents := make([]string, n)
	metaBytes := make([][]byte, n)

	for i, doc := range docs {
		ids[i] = doc.ID
		vecs[i] = embedding.Float32Vector(vectors[i])

		contents[i] = doc.Text

		meta, err := json.Marshal(doc.Metadata)
		if err != nil {
			return nil, fmt.Errorf("milvus: marshal metadata for document %s: %w", doc.ID, err)
		}
		metaBytes[i] = meta
	}

	dim := len(vecs[0])

	return []column.Column{
		column.NewColumnVarChar(fieldID, ids),
		column.NewColumnFloatVector(fieldVector, dim, vecs),
		column.NewColumnVarChar(fieldContent, contents),
		column.NewColumnJSONBytes(fieldMeta, metaBytes),
	}, nil
}

func (s *Store) Index(ctx context.Context, request *vectorstore.IndexRequest) (err error) {
	if validateErr := request.Validate(); validateErr != nil {
		return fmt.Errorf("milvus.Store.Index: %w", validateErr)
	}
	for index, doc := range request.Documents {
		if doc.Media != nil {
			return fmt.Errorf("milvus.Store.Index: %w: documents[%d] contains unsupported media", vectorstore.ErrInvalidDocument, index)
		}
	}
	if validateProviderDocumentsErr := validateProviderDocuments(request.Documents); validateProviderDocumentsErr != nil {
		return fmt.Errorf("milvus.Store.Index: %w", validateProviderDocumentsErr)
	}

	var batches []*vectorstore.IndexRequest
	batches, err = request.Batch(ctx, s.documentBatcher)
	if err != nil {
		return fmt.Errorf("milvus: batch documents: %w", err)
	}

	for _, batch := range batches {
		docs := batch.Documents
		texts, err := batch.Texts()
		if err != nil {
			return fmt.Errorf("vectorstore: project document text: %w", err)
		}
		vectors, err := s.embeddingClient.EmbedTexts(ctx, texts)
		if err != nil {
			return fmt.Errorf("milvus: embed documents: %w", err)
		}

		cols, err := s.buildInsertColumns(docs, vectors)
		if err != nil {
			return err
		}

		// UpsertCount is Milvus' acknowledgment of the batch. Accepting a short
		// count would report a partial write as a complete one.
		upserted, err := s.client.Upsert(ctx, milvusclient.NewColumnBasedInsertOption(s.collectionName, cols...))
		if err != nil {
			return fmt.Errorf("milvus: upsert %d documents to collection %s: %w",
				len(docs), s.collectionName, err)
		}
		if upserted.UpsertCount != int64(len(docs)) {
			return fmt.Errorf("milvus: upsert to collection %s acknowledged %d of %d documents",
				s.collectionName, upserted.UpsertCount, len(docs))
		}
	}

	return nil
}

func validateProviderDocuments(docs []*document.Document) error {
	for i, doc := range docs {
		if len(doc.ID) > maxIDLength {
			return fmt.Errorf("%w: documents[%d] has %d bytes", ErrDocumentIDTooLong, i, len(doc.ID))
		}
		if len(doc.Text) > maxContentLength {
			return fmt.Errorf("%w: documents[%d] has %d bytes", ErrDocumentContentTooLong, i, len(doc.Text))
		}
	}
	return nil
}

func (s *Store) buildDocumentsFromResults(rs milvusclient.ResultSet, minScore vectorstore.Score) ([]*vectorstore.SearchResult, error) {
	if len(rs.Scores) != rs.Len() {
		return nil, fmt.Errorf("milvus: search returned %d scores for %d rows", len(rs.Scores), rs.Len())
	}
	if rs.Len() == 0 {
		return nil, nil
	}
	docs := make([]*vectorstore.SearchResult, 0, rs.Len())

	idCol := rs.GetColumn(fieldID)
	contentCol := rs.GetColumn(fieldContent)
	metaCol := rs.GetColumn(fieldMeta)
	if idCol == nil || contentCol == nil || metaCol == nil {
		return nil, fmt.Errorf("milvus: search result is missing required output columns %q, %q, or %q",
			fieldID, fieldContent, fieldMeta)
	}

	for i := range rs.Len() {
		score := s.normalizeScore(float64(rs.Scores[i]))
		if score < minScore {
			continue
		}

		id, err := idCol.GetAsString(i)
		if err != nil {
			return nil, fmt.Errorf("milvus: read document ID for result %d: %w", i, err)
		}
		if id == "" {
			return nil, fmt.Errorf("milvus: result %d is missing document ID", i)
		}
		text, err := contentCol.GetAsString(i)
		if err != nil {
			return nil, fmt.Errorf("milvus: read document text for result %d: %w", i, err)
		}
		if text == "" {
			return nil, fmt.Errorf("milvus: result %d is missing document text", i)
		}

		raw, err := metaCol.Get(i)
		if err != nil {
			return nil, fmt.Errorf("milvus: read metadata for result %d: %w", i, err)
		}
		metaBytes, ok := raw.([]byte)
		if !ok {
			return nil, fmt.Errorf("milvus: metadata for result %d has type %T, want []byte", i, raw)
		}
		var decodedMetadata metadata.Map
		if err = json.Unmarshal(metaBytes, &decodedMetadata); err != nil {
			return nil, fmt.Errorf("milvus: decode metadata for result %d: %w", i, err)
		}

		doc := &document.Document{ID: id, Text: text, Metadata: decodedMetadata}
		docs = append(docs, &vectorstore.SearchResult{Document: doc, Score: score})
	}

	return docs, nil
}

// normalizeScore maps what Milvus returns for the collection's metric.
//
// The three metrics report three different things. COSINE is a cosine
// similarity in [-1, 1]. L2 is the squared distance — Milvus stops before the
// square root — which is monotone in the distance, so ranking survives even
// though the score is not comparable with another provider's. IP is the raw
// inner product with no normalization at all: it is unbounded unless the
// caller happens to supply unit vectors, so it cannot share the cosine
// mapping, which would clamp every product at or above 1 to a score of 1 and
// every product at or below -1 to 0, collapsing distinct results onto the same
// score and making MinScore meaningless.
func (s *Store) normalizeScore(raw float64) vectorstore.Score {
	switch s.metricType {
	case entity.L2:
		return vectorstore.ScoreFromDistance(raw)
	case entity.COSINE:
		return vectorstore.ScoreFromCosineSimilarity(raw)
	case entity.IP:
		return vectorstore.ScoreFromInnerProduct(raw)
	default:
		return vectorstore.ScoreFromValue(raw)
	}
}

func (s *Store) Search(ctx context.Context, req *vectorstore.SearchRequest) (response *vectorstore.SearchResponse, err error) {
	var docs []*vectorstore.SearchResult
	if err = req.Validate(); err != nil {
		return nil, fmt.Errorf("milvus.Store.Search: %w", err)
	}
	if err = req.Options.RequireMode(vectorstore.SearchModeSemantic); err != nil {
		return nil, fmt.Errorf("milvus.Store.Search: %w", err)
	}

	defer func() {
		if err == nil {
			err = response.ValidateFor(req)
		}
	}()

	vector, err := s.embeddingClient.EmbedText(ctx, req.Query)
	if err != nil {
		return nil, fmt.Errorf("milvus: embed query: %w", err)
	}

	queryVec := entity.FloatVector(embedding.Float32Vector(vector))

	searchOpt := milvusclient.NewSearchOption(s.collectionName, req.Options.ResultLimit(), []entity.Vector{queryVec}).
		WithANNSField(fieldVector).
		WithOutputFields(fieldID, fieldContent, fieldMeta)

	if req.Options.Filter != nil {
		visitor := newVisitor()
		if acceptErr := req.Options.Filter.Accept(visitor); acceptErr != nil {
			return nil, fmt.Errorf("milvus: convert filter: %w", acceptErr)
		}
		searchOpt = searchOpt.WithFilter(visitor.snapshot())
	}

	results, err := s.client.Search(ctx, searchOpt)
	if err != nil {
		return nil, fmt.Errorf("milvus: search collection %s: %w", s.collectionName, err)
	}

	if len(results) == 0 {
		return nil, nil
	}

	docs, err = s.buildDocumentsFromResults(results[0], req.Options.MinScore)
	if err != nil {
		return nil, fmt.Errorf("milvus: build documents from results: %w", err)
	}

	return &vectorstore.SearchResponse{Results: docs}, nil
}

func (s *Store) DeleteWhere(ctx context.Context, expr filter.Predicate) (err error) {
	if expr == nil {
		return vectorstore.ErrMissingFilter
	}
	if err = expr.Validate(); err != nil {
		return fmt.Errorf("milvus.Store.DeleteWhere: %w", err)
	}

	visitor := newVisitor()
	if err = expr.Accept(visitor); err != nil {
		return fmt.Errorf("milvus: convert filter: %w", err)
	}

	_, err = s.client.Delete(ctx, milvusclient.NewDeleteOption(s.collectionName).WithExpr(visitor.snapshot()))
	if err != nil {
		return fmt.Errorf("milvus: delete from collection %s: %w", s.collectionName, err)
	}

	return nil
}

// DeleteIDs removes rows by primary key. WithStringIDs compiles to the
// expr `id in ["a","b"]`, so unknown ids are silently ignored (idempotent).
// An empty slice is a no-op. Implements [vectorstore.IDDeleter].
func (s *Store) DeleteIDs(ctx context.Context, ids []string) (err error) {
	if len(ids) == 0 {
		return nil
	}

	_, err = s.client.Delete(ctx, milvusclient.NewDeleteOption(s.collectionName).WithStringIDs(fieldID, ids))
	if err != nil {
		return fmt.Errorf("milvus: delete by ids from collection %s: %w", s.collectionName, err)
	}

	return nil
}

func (s *Store) Close(ctx context.Context) error {
	return s.client.Close(ctx)
}
