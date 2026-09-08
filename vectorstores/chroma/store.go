package chroma

import (
	"context"
	"errors"
	"fmt"

	v2 "github.com/amikos-tech/chroma-go/pkg/api/v2"
	chromaEmbed "github.com/amikos-tech/chroma-go/pkg/embeddings"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/embeddingclient"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

// Provider is the stable backend name for host-side attribution.
const Provider = "Chroma"

// DistanceMetric defines the distance function used by the HNSW index.
type DistanceMetric string

const (
	// DistanceCosine uses cosine distance (1 - cosine_similarity).
	// Returned distances are in [0, 2]; lower means more similar.
	DistanceCosine DistanceMetric = "cosine"
	// DistanceL2 uses squared L2 (Euclidean) distance.
	// Returned distances are in [0, ∞); lower means more similar.
	DistanceL2 DistanceMetric = "l2"
	// DistanceIP uses inner product (dot product) distance.
	// Returned values are in (-∞, ∞); higher means more similar.
	DistanceIP DistanceMetric = "ip"
)

func (d DistanceMetric) Valid() bool {
	switch d {
	case DistanceCosine, DistanceL2, DistanceIP:
		return true
	default:
		return false
	}
}

func (d DistanceMetric) String() string { return string(d) }

// score converts a Chroma distance value into a similarity score in which
// higher values indicate greater similarity.
func (d DistanceMetric) score(distance float64) vectorstore.Score {
	switch d {
	case DistanceCosine:
		return vectorstore.ScoreFromCosineDistance(distance)
	case DistanceL2:
		return vectorstore.ScoreFromDistance(distance)
	case DistanceIP:
		// Chroma reports 1 - inner product as a distance.
		return vectorstore.ScoreFromOneMinusInnerProductDistance(distance)
	default:
		return vectorstore.ScoreFromValue(distance)
	}
}

// StoreConfig contains configuration options for the Chroma vector store.
type StoreConfig struct {
	// Client is the Chroma HTTP client.
	// Required: must be provided, otherwise initialization will fail.
	Client v2.Client

	// CollectionName is the name of the Chroma collection to use.
	// Required: must be a non-empty string.
	CollectionName string

	// InitializeSchema indicates whether to automatically create the collection
	// if it does not exist. When true, GetOrCreateCollection is used; otherwise
	// the collection must already exist.
	// Optional: defaults to false.
	InitializeSchema bool

	// DistanceMetric is the HNSW distance function applied when the collection
	// is created via InitializeSchema. Has no effect on an existing collection.
	// Optional: defaults to DistanceCosine.
	DistanceMetric DistanceMetric

	// EmbeddingModel is the model used to generate vector embeddings from text.
	// Required: must be provided.
	EmbeddingModel embedding.Model

	// DocumentBatcher is responsible for batching documents before insertion.
	// Required: must be provided.
	DocumentBatcher vectorstore.Batcher
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
	if !s.DistanceMetric.Valid() {
		return fmt.Errorf("chroma: unsupported DistanceMetric %q", s.DistanceMetric)
	}
	return nil
}

// applyDefaults fills zero fields. DistanceMetric defaults to
// [DistanceCosine].
func (s *StoreConfig) applyDefaults() {
	if s.DistanceMetric == "" {
		s.DistanceMetric = DistanceCosine
	}
}

var (
	_ vectorstore.Indexer       = (*Store)(nil)
	_ vectorstore.Searcher      = (*Store)(nil)
	_ vectorstore.FilterDeleter = (*Store)(nil)
	_ vectorstore.IDDeleter     = (*Store)(nil)
)

// Store is a Chroma-backed implementation of vectorstore capability interfaces.
type Store struct {
	client          v2.Client
	collection      v2.Collection
	collectionName  string
	embeddingClient embeddingclient.Client
	documentBatcher vectorstore.Batcher
	distanceMetric  DistanceMetric
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
		return nil, fmt.Errorf("chroma: create embedding client: %w", err)
	}

	store := &Store{
		client:          config.Client,
		collectionName:  config.CollectionName,
		embeddingClient: embeddingClient,
		documentBatcher: config.DocumentBatcher,
		distanceMetric:  config.DistanceMetric,
	}

	if err = store.initialize(ctx, config.InitializeSchema); err != nil {
		return nil, fmt.Errorf("chroma: initialize vector store: %w", err)
	}

	return store, nil
}

func (s *Store) initialize(ctx context.Context, initializeSchema bool) error {
	var (
		col v2.Collection
		err error
	)

	if initializeSchema {
		col, err = s.client.GetOrCreateCollection(
			ctx,
			s.collectionName,
			v2.WithHNSWSpaceCreate(chromaEmbed.DistanceMetric(s.distanceMetric)),
		)
	} else {
		col, err = s.client.GetCollection(ctx, s.collectionName)
	}
	if err != nil {
		return fmt.Errorf("chroma: get/create collection %s: %w", s.collectionName, err)
	}

	s.collection = col
	return nil
}

// metadataToMap converts a Chroma DocumentMetadata into a plain map.
// It type-asserts to the concrete *DocumentMetadataImpl to access Keys() and
// the typed getters, preserving the original value types.
func metadataToMap(meta v2.DocumentMetadata) map[string]any {
	if meta == nil {
		return nil
	}
	impl, ok := meta.(*v2.DocumentMetadataImpl)
	if !ok {
		return nil
	}
	keys := impl.Keys()
	if len(keys) == 0 {
		return nil
	}
	result := make(map[string]any, len(keys))
	for _, key := range keys {
		if value, present := metadataValue(impl, key); present {
			result[key] = value
		}
	}
	return result
}

func metadataValue(values *v2.DocumentMetadataImpl, key string) (any, bool) {
	if value, present := values.GetString(key); present {
		return value, true
	}
	if value, present := values.GetInt(key); present {
		return value, true
	}
	if value, present := values.GetFloat(key); present {
		return value, true
	}
	if value, present := values.GetBool(key); present {
		return value, true
	}
	if value, present := values.GetStringArray(key); present {
		return value, true
	}
	if value, present := values.GetIntArray(key); present {
		return value, true
	}
	if value, present := values.GetFloatArray(key); present {
		return value, true
	}
	return values.GetBoolArray(key)
}

// buildAddOptions assembles the Upsert options for a single document batch
// together with their pre-computed embedding vectors.
func (s *Store) buildAddOptions(docs []*document.Document, vectors [][]float64) ([]v2.CollectionAddOption, error) {
	ids := make([]v2.DocumentID, 0, len(docs))
	embs := make([]chromaEmbed.Embedding, 0, len(docs))
	metadatas := make([]v2.DocumentMetadata, 0, len(docs))
	texts := make([]string, 0, len(docs))

	for i, doc := range docs {
		ids = append(ids, v2.DocumentID(doc.ID))

		f32 := embedding.Float32Vector(vectors[i])
		embs = append(embs, chromaEmbed.NewEmbeddingFromFloat32(f32))

		metadataValues, err := doc.Metadata.Values()
		if err != nil {
			return nil, fmt.Errorf("chroma: decode metadata for document %d: %w", i, err)
		}
		meta, err := v2.NewDocumentMetadataFromMap(metadataValues)
		if err != nil {
			return nil, fmt.Errorf("chroma: convert metadata for document %d: %w", i, err)
		}
		metadatas = append(metadatas, meta)

		texts = append(texts, doc.Text)
	}

	opts := []v2.CollectionAddOption{
		v2.WithIDs(ids...),
		v2.WithEmbeddings(embs...),
		v2.WithMetadatas(metadatas...),
		v2.WithTexts(texts...),
	}

	return opts, nil
}

// Index embeds the documents and upserts them into Chroma.
func (s *Store) Index(ctx context.Context, request *vectorstore.IndexRequest) (err error) {
	if validateErr := request.Validate(); validateErr != nil {
		return fmt.Errorf("chroma.Store.Index: %w", validateErr)
	}

	var batches []*vectorstore.IndexRequest
	batches, err = request.Batch(ctx, s.documentBatcher)
	if err != nil {
		return fmt.Errorf("chroma: batch documents: %w", err)
	}

	for _, batch := range batches {
		docs := batch.Documents
		texts, err := batch.Texts()
		if err != nil {
			return fmt.Errorf("vectorstore: project document text: %w", err)
		}
		vectors, err := s.embeddingClient.EmbedTexts(ctx, texts)
		if err != nil {
			return fmt.Errorf("chroma: embed documents: %w", err)
		}

		opts, err := s.buildAddOptions(docs, vectors)
		if err != nil {
			return err
		}

		if err = s.collection.Upsert(ctx, opts...); err != nil {
			return fmt.Errorf("chroma: upsert documents into collection %s: %w",
				s.collectionName, err)
		}
	}

	return nil
}

// buildQueryOptions assembles the Query options for the given retrieval request
// and the pre-computed query embedding vector.
func (s *Store) buildQueryOptions(req *vectorstore.SearchRequest, queryVector []float32) ([]v2.CollectionQueryOption, error) {
	queryEmb := chromaEmbed.NewEmbeddingFromFloat32(queryVector)

	opts := []v2.CollectionQueryOption{
		v2.WithQueryEmbeddings(queryEmb),
		v2.WithNResults(req.Options.ResultLimit()),
		v2.WithInclude(v2.IncludeDocuments, v2.IncludeMetadatas, v2.IncludeDistances),
	}

	if req.Options.Filter != nil {
		visitor := newVisitor()
		if err := req.Options.Filter.Accept(visitor); err != nil {
			return nil, fmt.Errorf("chroma: convert filter: %w", err)
		}
		if result := visitor.snapshot(); result != nil {
			opts = append(opts, v2.WithWhere(result))
		}
	}

	return opts, nil
}

// buildDocumentsFromResult assembles Scope Documents from the parallel slices
// returned by the QueryResult interface, applying the MinScore threshold.
func (s *Store) buildDocumentsFromResult(result v2.QueryResult, minScore vectorstore.Score) ([]*vectorstore.SearchResult, error) {
	idGroups := result.GetIDGroups()
	if len(idGroups) == 0 {
		return nil, nil
	}
	if len(idGroups) != 1 {
		return nil, fmt.Errorf("chroma: query returned %d ID groups for one query vector", len(idGroups))
	}

	ids := idGroups[0]

	var docGroup v2.Documents
	if dg := result.GetDocumentsGroups(); len(dg) > 0 {
		if len(dg) != 1 {
			return nil, fmt.Errorf("chroma: query returned %d document groups for one query vector", len(dg))
		}
		docGroup = dg[0]
	}
	if len(docGroup) != len(ids) {
		return nil, fmt.Errorf("chroma: query returned %d documents for %d IDs", len(docGroup), len(ids))
	}

	var metaGroup v2.DocumentMetadatas
	if mg := result.GetMetadatasGroups(); len(mg) > 0 {
		if len(mg) != 1 {
			return nil, fmt.Errorf("chroma: query returned %d metadata groups for one query vector", len(mg))
		}
		metaGroup = mg[0]
	}
	if len(metaGroup) != len(ids) {
		return nil, fmt.Errorf("chroma: query returned %d metadata values for %d IDs", len(metaGroup), len(ids))
	}

	var distGroup chromaEmbed.Distances
	if dg := result.GetDistancesGroups(); len(dg) > 0 {
		if len(dg) != 1 {
			return nil, fmt.Errorf("chroma: query returned %d distance groups for one query vector", len(dg))
		}
		distGroup = dg[0]
	}
	if len(distGroup) != len(ids) {
		return nil, fmt.Errorf("chroma: query returned %d distances for %d IDs", len(distGroup), len(ids))
	}

	docs := make([]*vectorstore.SearchResult, 0, len(ids))
	for i, id := range ids {
		if id == "" {
			return nil, fmt.Errorf("chroma: query result %d is missing ID", i)
		}
		distance := float64(distGroup[i])
		score := s.distanceMetric.score(distance)
		if score < minScore {
			continue
		}

		if docGroup[i] == nil || docGroup[i].ContentString() == "" {
			return nil, fmt.Errorf("chroma: query result %d is missing document text", i)
		}
		doc := &document.Document{ID: string(id), Text: docGroup[i].ContentString()}

		if i < len(metaGroup) && metaGroup[i] != nil {
			var err error
			doc.Metadata, err = metadata.FromValues(metadataToMap(metaGroup[i]))
			if err != nil {
				return nil, fmt.Errorf("chroma: convert metadata: %w", err)
			}
		}

		docs = append(docs, &vectorstore.SearchResult{Document: doc, Score: score})
	}

	return docs, nil
}

// Search embeds the query, searches Chroma, and returns matching documents.
func (s *Store) Search(ctx context.Context, req *vectorstore.SearchRequest) (response *vectorstore.SearchResponse, err error) {
	var docs []*vectorstore.SearchResult
	if err = req.Validate(); err != nil {
		return nil, fmt.Errorf("chroma.Store.Search: %w", err)
	}
	if err = req.Options.RequireMode(vectorstore.SearchModeSemantic); err != nil {
		return nil, fmt.Errorf("chroma.Store.Search: %w", err)
	}

	defer func() {
		if err == nil {
			err = response.ValidateFor(req)
		}
	}()

	vector, err := s.embeddingClient.EmbedText(ctx, req.Query)
	if err != nil {
		return nil, fmt.Errorf("chroma: embed query: %w", err)
	}

	queryVector := embedding.Float32Vector(vector)

	var opts []v2.CollectionQueryOption
	opts, err = s.buildQueryOptions(req, queryVector)
	if err != nil {
		return nil, err
	}

	var result v2.QueryResult
	result, err = s.collection.Query(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("chroma: query collection %s: %w", s.collectionName, err)
	}

	docs, err = s.buildDocumentsFromResult(result, req.Options.MinScore)
	if err != nil {
		return nil, err
	}
	return &vectorstore.SearchResponse{Results: docs}, nil
}

func (s *Store) DeleteWhere(ctx context.Context, expr filter.Predicate) (err error) {
	if expr == nil {
		return vectorstore.ErrMissingFilter
	}
	if err = expr.Validate(); err != nil {
		return fmt.Errorf("chroma.Store.DeleteWhere: %w", err)
	}

	visitor := newVisitor()
	if err = expr.Accept(visitor); err != nil {
		return fmt.Errorf("chroma: convert filter: %w", err)
	}

	// A Chroma delete carrying neither ids nor a where clause selects the whole
	// collection, so the store refuses it here. Leaving the refusal to the
	// client library would make the blast radius of a filter that compiles to
	// nothing depend on a dependency's validation.
	where := visitor.snapshot()
	if where == nil {
		return errors.New("chroma: refusing to delete on empty filter")
	}

	if err = s.collection.Delete(ctx, v2.WithWhere(where)); err != nil {
		return fmt.Errorf("chroma: delete documents from collection %s: %w",
			s.collectionName, err)
	}

	return nil
}

// DeleteIDs removes documents from the collection by their Chroma IDs.
// An empty slice is a no-op; unknown ids are silently ignored. Implements
// [vectorstore.IDDeleter].
func (s *Store) DeleteIDs(ctx context.Context, ids []string) (err error) {
	if len(ids) == 0 {
		return nil
	}

	docIDs := make([]v2.DocumentID, len(ids))
	for i, id := range ids {
		docIDs[i] = v2.DocumentID(id)
	}

	if err = s.collection.Delete(ctx, v2.WithIDs(docIDs...)); err != nil {
		return fmt.Errorf("chroma: delete documents by ids from collection %s: %w",
			s.collectionName, err)
	}

	return nil
}

// Close releases resources held by the underlying Chroma collection handle.
func (s *Store) Close() error {
	return s.collection.Close()
}
