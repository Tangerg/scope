package mongodb

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/embeddingclient"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

// Provider is the stable backend name for host-side attribution.
const Provider = "MongoDB"

// Exported defaults keep constructor behavior visible and overridable.
const (
	DefaultVectorIndexName = "vector_index"
	DefaultEmbeddingPath   = "embedding"
	DefaultContentField    = "content"
	DefaultMetadataField   = "metadata"
	DefaultNumCandidates   = 200
	defaultIDField         = "_id"
	scoreField             = "score"
)

// MaxNumCandidates is Atlas's ceiling for the $vectorSearch numCandidates
// field, and so the largest TopK the store can serve.
const MaxNumCandidates = 10000

// Similarity selects the vector similarity function written into the
// Atlas Vector Search index definition.
type Similarity string

const (
	// SimilarityCosine — cosine similarity. Default.
	SimilarityCosine Similarity = "cosine"

	// SimilarityEuclidean — Euclidean (L2) distance.
	SimilarityEuclidean Similarity = "euclidean"

	// SimilarityDotProduct — dot product (best for normalized
	// embeddings).
	SimilarityDotProduct Similarity = "dotProduct"
)

func (s Similarity) Valid() bool {
	switch s {
	case SimilarityCosine, SimilarityEuclidean, SimilarityDotProduct:
		return true
	default:
		return false
	}
}

func (s Similarity) String() string { return string(s) }

// DocumentCollection is the MongoDB surface the store uses: the batched
// upsert, the aggregation that runs $vectorSearch, the deletion both delete
// paths share, and the search-index view schema initialization needs. A
// [mongo.Collection] satisfies it. Naming only these four keeps the store's own
// write accounting checkable without an Atlas cluster.
type DocumentCollection interface {
	BulkWrite(
		ctx context.Context,
		models []mongo.WriteModel,
		opts ...options.Lister[options.BulkWriteOptions],
	) (*mongo.BulkWriteResult, error)
	Aggregate(
		ctx context.Context,
		pipeline any,
		opts ...options.Lister[options.AggregateOptions],
	) (*mongo.Cursor, error)
	DeleteMany(
		ctx context.Context,
		filter any,
		opts ...options.Lister[options.DeleteManyOptions],
	) (*mongo.DeleteResult, error)
	SearchIndexes() mongo.SearchIndexView
}

// StoreConfig contains configuration options for the MongoDB Atlas
// Vector Search store.
type StoreConfig struct {
	// Collection is the MongoDB collection that holds the documents.
	// Required.
	Collection DocumentCollection

	// VectorIndexName is the Atlas Vector Search index name. It must
	// match an existing index (or one created by InitializeSchema).
	// Optional: defaults to [DefaultVectorIndexName].
	VectorIndexName string

	// EmbeddingPath is the field that holds the document embedding.
	// Optional: defaults to [DefaultEmbeddingPath] ("embedding").
	EmbeddingPath string

	// ContentField is the field that stores the original text.
	// Optional: defaults to [DefaultContentField].
	ContentField string

	// MetadataField is the sub-document field that holds metadata.
	// Optional: defaults to [DefaultMetadataField]. Metadata is always isolated
	// in this sub-document so user keys cannot collide with storage fields.
	MetadataField string

	// MetadataFieldsToFilter pre-declares the metadata keys that
	// should be indexed as filter fields in the Atlas search index.
	// Filtering on a metadata field requires the field to be listed
	// here when InitializeSchema is true.
	MetadataFieldsToFilter []string

	// EmbeddingModel produces vectors for the documents. Required.
	EmbeddingModel embedding.Model

	// DocumentBatcher batches documents before upsert. Required.
	DocumentBatcher vectorstore.Batcher

	// Dimensions is the embedding width written into a new search-index
	// definition. When zero and InitializeSchema is true, the store probes
	// EmbeddingModel.
	Dimensions int

	// Similarity selects the vector similarity function. Optional:
	// defaults to [SimilarityCosine].
	Similarity Similarity

	// NumCandidates controls the recall/perf tradeoff of the Atlas
	// $vectorSearch stage. It is a floor: a search never considers fewer
	// candidates than the results it must return. Optional: defaults to
	// [DefaultNumCandidates] (200), and must not exceed [MaxNumCandidates].
	NumCandidates int

	// InitializeSchema, when true, creates the Atlas vector-search
	// index if it doesn't already exist. Requires a connected Atlas
	// cluster.
	InitializeSchema bool
}

func (s StoreConfig) Validate() error {
	s.applyDefaults()
	if lo.IsNil(s.Collection) {
		return errors.New("mongodb: Collection is required")
	}
	if lo.IsNil(s.EmbeddingModel) {
		return errors.New("mongodb: EmbeddingModel is required")
	}
	if lo.IsNil(s.DocumentBatcher) {
		return errors.New("mongodb: DocumentBatcher is required")
	}
	if s.Dimensions < 0 {
		return errors.New("mongodb: Dimensions must be >= 0")
	}
	if s.NumCandidates > MaxNumCandidates {
		return fmt.Errorf("mongodb: NumCandidates must be <= %d", MaxNumCandidates)
	}
	if !s.Similarity.Valid() {
		return fmt.Errorf("mongodb: unsupported Similarity %q", s.Similarity)
	}
	if err := s.validateFieldLayout(); err != nil {
		return err
	}
	if err := s.validateMetadataFields(); err != nil {
		return err
	}
	return nil
}

func (s StoreConfig) validateFieldLayout() error {
	fields := []struct {
		name  string
		value string
	}{
		{name: "reserved _id", value: defaultIDField},
		{name: "reserved query score", value: scoreField},
		{name: "ContentField", value: s.ContentField},
		{name: "EmbeddingPath", value: s.EmbeddingPath},
		{name: "MetadataField", value: s.MetadataField},
	}
	seen := make(map[string]string, len(fields))
	for _, field := range fields {
		if owner, duplicate := seen[field.value]; duplicate {
			return fmt.Errorf("mongodb: %s and %s both use field %q", owner, field.name, field.value)
		}
		seen[field.value] = field.name
	}
	return nil
}

func (s StoreConfig) validateMetadataFields() error {
	seen := make(map[string]struct{}, len(s.MetadataFieldsToFilter))
	for index, field := range s.MetadataFieldsToFilter {
		if field == "" {
			return fmt.Errorf("mongodb: MetadataFieldsToFilter[%d] must not be empty", index)
		}
		if _, duplicate := seen[field]; duplicate {
			return fmt.Errorf("mongodb: MetadataFieldsToFilter[%d] duplicates %q", index, field)
		}
		seen[field] = struct{}{}
	}
	return nil
}

// applyDefaults fills zero fields with documented defaults.
func (s *StoreConfig) applyDefaults() {
	s.VectorIndexName = cmp.Or(s.VectorIndexName, DefaultVectorIndexName)
	s.EmbeddingPath = cmp.Or(s.EmbeddingPath, DefaultEmbeddingPath)
	s.ContentField = cmp.Or(s.ContentField, DefaultContentField)
	s.MetadataField = cmp.Or(s.MetadataField, DefaultMetadataField)
	if s.NumCandidates <= 0 {
		s.NumCandidates = DefaultNumCandidates
	}
	s.Similarity = cmp.Or(s.Similarity, SimilarityCosine)
}

var (
	_ vectorstore.Indexer       = (*Store)(nil)
	_ vectorstore.Searcher      = (*Store)(nil)
	_ vectorstore.FilterDeleter = (*Store)(nil)
	_ vectorstore.IDDeleter     = (*Store)(nil)
)

// Store implements vector-store capabilities with MongoDB Atlas Vector Search.
type Store struct {
	collection             DocumentCollection
	vectorIndexName        string
	embeddingPath          string
	contentField           string
	metadataField          string
	metadataFieldsToFilter []string
	embeddingClient        embeddingclient.Client
	documentBatcher        vectorstore.Batcher
	dimensions             int
	similarity             Similarity
	numCandidates          int
}

// NewStore performs schema setup during construction, which is why it takes
// a context: a store returned before its collection and search index exist
// would fail on the first index rather than at wiring, where the
// misconfiguration actually is.
func NewStore(ctx context.Context, config StoreConfig) (*Store, error) {
	config.applyDefaults()
	if err := config.Validate(); err != nil {
		return nil, err
	}

	embeddingClient, err := embeddingclient.New(config.EmbeddingModel)
	if err != nil {
		return nil, fmt.Errorf("mongodb: create embedding client: %w", err)
	}

	store := &Store{
		collection:             config.Collection,
		vectorIndexName:        config.VectorIndexName,
		embeddingPath:          config.EmbeddingPath,
		contentField:           config.ContentField,
		metadataField:          config.MetadataField,
		metadataFieldsToFilter: slices.Clone(config.MetadataFieldsToFilter),
		embeddingClient:        embeddingClient,
		documentBatcher:        config.DocumentBatcher,
		dimensions:             config.Dimensions,
		similarity:             config.Similarity,
		numCandidates:          config.NumCandidates,
	}

	if err = store.initialize(ctx, config.InitializeSchema); err != nil {
		return nil, fmt.Errorf("mongodb: initialize store: %w", err)
	}
	return store, nil
}

// initialize creates the Atlas vector
// index when requested.
func (s *Store) initialize(ctx context.Context, initSchema bool) error {
	if !initSchema {
		return nil
	}
	if s.dimensions <= 0 {
		return errors.New("mongodb: Dimensions must be > 0")
	}

	return s.createSearchIndex(ctx)
}

func (s *Store) createSearchIndex(ctx context.Context) error {
	cursor, err := s.collection.SearchIndexes().List(ctx, options.SearchIndexes().SetName(s.vectorIndexName))
	if err != nil {
		return fmt.Errorf("mongodb: list search index %q: %w", s.vectorIndexName, err)
	}
	defer cursor.Close(ctx)
	if cursor.Next(ctx) {
		return nil // already exists
	}
	if err := cursor.Err(); err != nil {
		return fmt.Errorf("mongodb: read search indexes: %w", err)
	}

	fields := []bson.M{
		{
			"type":          "vector",
			"path":          s.embeddingPath,
			"numDimensions": s.dimensions,
			"similarity":    string(s.similarity),
		},
	}
	for _, name := range s.metadataFieldsToFilter {
		fields = append(fields, bson.M{
			"type": "filter",
			"path": s.metadataField + "." + name,
		})
	}

	definition := bson.M{"fields": fields}
	model := mongo.SearchIndexModel{
		Definition: definition,
		Options:    options.SearchIndexes().SetName(s.vectorIndexName).SetType("vectorSearch"),
	}
	if _, err := s.collection.SearchIndexes().CreateOne(ctx, model); err != nil {
		return fmt.Errorf("createSearchIndexes: %w", err)
	}
	return nil
}

// Index embeds documents and bulk-upserts them by _id.
func (s *Store) Index(ctx context.Context, request *vectorstore.IndexRequest) (err error) {
	if validateErr := request.Validate(); validateErr != nil {
		return fmt.Errorf("mongodb.Store.Index: %w", validateErr)
	}
	for index, doc := range request.Documents {
		if doc.Media != nil {
			return fmt.Errorf("mongodb.Store.Index: %w: documents[%d] contains unsupported media", vectorstore.ErrInvalidDocument, index)
		}
	}

	var batches []*vectorstore.IndexRequest
	batches, err = request.Batch(ctx, s.documentBatcher)
	if err != nil {
		return fmt.Errorf("mongodb: batch documents: %w", err)
	}

	for _, batch := range batches {
		docs := batch.Documents
		texts, err := batch.Texts()
		if err != nil {
			return fmt.Errorf("vectorstore: project document text: %w", err)
		}
		vectors, err := s.embeddingClient.EmbedTexts(ctx, texts)
		if err != nil {
			return fmt.Errorf("mongodb: embed documents: %w", err)
		}

		writes := make([]mongo.WriteModel, 0, len(docs))
		for i, doc := range docs {
			id := doc.ID
			metadataValues, err := doc.Metadata.Values()
			if err != nil {
				return fmt.Errorf("mongodb: decode metadata for %s: %w", id, err)
			}

			payload := bson.M{
				defaultIDField:  id,
				s.contentField:  doc.Text,
				s.embeddingPath: embedding.Float32Vector(vectors[i]),
			}
			payload[s.metadataField], err = metadataDocument(metadataValues)
			if err != nil {
				return fmt.Errorf("mongodb: convert metadata for %s: %w", id, err)
			}

			writes = append(writes, mongo.NewReplaceOneModel().
				SetFilter(bson.M{defaultIDField: id}).
				SetReplacement(payload).
				SetUpsert(true),
			)
		}

		result, writeErr := s.collection.BulkWrite(ctx, writes)
		if writeErr != nil {
			return fmt.Errorf("mongodb: BulkWrite: %w", writeErr)
		}
		if err := checkBulkAcknowledgment(result, len(writes)); err != nil {
			return err
		}
	}
	return nil
}

// checkBulkAcknowledgment accounts for every replacement in one batch.
//
// The driver reports a replacement that found an existing document under
// MatchedCount and one that inserted a new document under UpsertedCount, so
// their sum is the server's count of applied writes and must cover the batch. A
// nil error alone does not: under an unacknowledged write concern MongoDB
// answers without a reply at all, and the driver then returns a zero-valued
// result whose other fields, in its own words, "may not be deterministic".
func checkBulkAcknowledgment(result *mongo.BulkWriteResult, sent int) error {
	if result == nil {
		return errors.New("mongodb: bulk write returned no result")
	}
	if !result.Acknowledged {
		return errors.New(
			"mongodb: collection writes are unacknowledged (w: 0), so an upsert cannot be confirmed",
		)
	}
	if applied := result.MatchedCount + result.UpsertedCount; applied != int64(sent) {
		return fmt.Errorf("mongodb: bulk write applied %d of %d documents", applied, sent)
	}
	return nil
}

// Search runs the $vectorSearch aggregation and returns the matching
// documents above the configured MinScore threshold.
func (s *Store) Search(ctx context.Context, req *vectorstore.SearchRequest) (response *vectorstore.SearchResponse, err error) {
	var docs []*vectorstore.SearchResult
	if err = req.Validate(); err != nil {
		return nil, fmt.Errorf("mongodb.Store.Search: %w", err)
	}
	if err = req.Options.RequireMode(vectorstore.SearchModeSemantic); err != nil {
		return nil, fmt.Errorf("mongodb.Store.Search: %w", err)
	}

	defer func() {
		if err == nil {
			err = response.ValidateFor(req)
		}
	}()

	vector, err := s.embeddingClient.EmbedText(ctx, req.Query)
	if err != nil {
		return nil, fmt.Errorf("mongodb: embed query: %w", err)
	}
	queryVec := embedding.Float32Vector(vector)

	candidates, err := s.searchCandidates(req.Options.ResultLimit())
	if err != nil {
		return nil, err
	}
	vectorSearch := bson.M{
		"index":         s.vectorIndexName,
		"path":          s.embeddingPath,
		"queryVector":   queryVec,
		"numCandidates": candidates,
		"limit":         req.Options.ResultLimit(),
	}
	if req.Options.Filter != nil {
		filterDoc, filterErr := s.buildFilter(req.Options.Filter)
		if filterErr != nil {
			return nil, filterErr
		}
		if len(filterDoc) > 0 {
			vectorSearch["filter"] = filterDoc
		}
	}

	pipeline := mongo.Pipeline{
		{{Key: "$vectorSearch", Value: vectorSearch}},
		{{Key: "$addFields", Value: bson.M{
			scoreField: bson.M{"$meta": "vectorSearchScore"},
		}}},
	}
	if req.Options.MinScore > 0 {
		pipeline = append(pipeline, bson.D{
			{Key: "$match", Value: bson.M{scoreField: bson.M{"$gte": req.Options.MinScore}}},
		})
	}

	cursor, err := s.collection.Aggregate(ctx, pipeline)
	if err != nil {
		return nil, fmt.Errorf("mongodb: aggregate: %w", err)
	}
	defer cursor.Close(ctx)

	docs = make([]*vectorstore.SearchResult, 0, req.Options.ResultLimit())
	for cursor.Next(ctx) {
		var raw bson.M
		if err := cursor.Decode(&raw); err != nil {
			return nil, fmt.Errorf("mongodb: decode hit: %w", err)
		}
		match, err := s.toMatch(raw)
		if err != nil {
			return nil, err
		}
		docs = append(docs, match)
	}
	if err := cursor.Err(); err != nil {
		return nil, fmt.Errorf("mongodb: cursor: %w", err)
	}
	return &vectorstore.SearchResponse{Results: docs}, nil
}

func (s *Store) DeleteWhere(ctx context.Context, expr filter.Predicate) (err error) {
	if expr == nil {
		return vectorstore.ErrMissingFilter
	}
	if err = expr.Validate(); err != nil {
		return fmt.Errorf("mongodb.Store.DeleteWhere: %w", err)
	}

	var filter bson.M
	filter, err = s.buildFilter(expr)
	if err != nil {
		return err
	}
	if len(filter) == 0 {
		return errors.New("mongodb: refusing to delete on empty filter")
	}

	if _, err := s.collection.DeleteMany(ctx, filter); err != nil {
		return fmt.Errorf("mongodb: DeleteMany: %w", err)
	}
	return nil
}

// DeleteIDs removes documents by their _id — `DeleteMany({_id: {$in: ids}})`.
// An empty slice is a no-op; unknown ids are silently ignored (idempotent).
// Implements [vectorstore.IDDeleter].
func (s *Store) DeleteIDs(ctx context.Context, ids []string) (err error) {
	if len(ids) == 0 {
		return nil
	}

	if _, err = s.collection.DeleteMany(ctx, bson.M{defaultIDField: bson.M{"$in": ids}}); err != nil {
		return fmt.Errorf("mongodb: DeleteMany by ids: %w", err)
	}
	return nil
}

// searchCandidates resolves how much of the vector index one query explores.
//
// numCandidates is not independent of the requested result count: it sizes the
// priority queue the search fills, so Atlas rejects a value below limit — a
// queue cannot yield more results than it holds — and caps it at
// MaxNumCandidates. StoreConfig.NumCandidates therefore sets a recall floor
// rather than the value sent, and a TopK past the ceiling is refused here
// instead of becoming a provider validation error.
func (s *Store) searchCandidates(limit int) (int, error) {
	if limit > MaxNumCandidates {
		return 0, fmt.Errorf(
			"mongodb.Store.Search: TopK %d exceeds the %d candidates $vectorSearch can consider",
			limit, MaxNumCandidates,
		)
	}
	return min(max(s.numCandidates, limit), MaxNumCandidates), nil
}

// buildFilter runs the AST through the visitor and returns the
// MongoDB filter document.
func (s *Store) buildFilter(expr filter.Predicate) (bson.M, error) {
	if expr == nil {
		return nil, nil
	}
	v := newVisitor(s.metadataField)
	if err := expr.Accept(v); err != nil {
		return nil, fmt.Errorf("mongodb: convert filter: %w", err)
	}
	return bson.M(v.snapshot()), nil
}

func (s *Store) toMatch(raw bson.M) (*vectorstore.SearchResult, error) {
	id, ok := raw[defaultIDField].(string)
	if !ok || id == "" {
		return nil, fmt.Errorf("mongodb: result is missing string field %q", defaultIDField)
	}
	content, ok := raw[s.contentField].(string)
	if !ok || content == "" {
		return nil, fmt.Errorf("mongodb: result is missing string field %q", s.contentField)
	}
	doc := &document.Document{ID: id, Text: content}
	var rawScore float64
	switch value := raw[scoreField].(type) {
	case float64:
		rawScore = value
	case float32:
		rawScore = float64(value)
	default:
		return nil, fmt.Errorf("mongodb: result score has type %T, want number", raw[scoreField])
	}
	score := vectorstore.ScoreFromValue(rawScore)

	metadataValues, err := s.metadataValues(raw)
	if err != nil {
		return nil, err
	}
	doc.Metadata, err = metadata.FromValues(metadataValues)
	if err != nil {
		return nil, fmt.Errorf("mongodb: convert metadata: %w", err)
	}
	return &vectorstore.SearchResult{Document: doc, Score: score}, nil
}

func (s *Store) metadataValues(raw bson.M) (map[string]any, error) {
	value, present := raw[s.metadataField]
	if !present {
		return nil, nil
	}
	switch value := value.(type) {
	case bson.M:
		return map[string]any(value), nil
	case map[string]any:
		return value, nil
	case bson.D:
		values := make(map[string]any, len(value))
		for _, element := range value {
			if _, duplicate := values[element.Key]; duplicate {
				return nil, fmt.Errorf("mongodb: result metadata contains duplicate key %q", element.Key)
			}
			values[element.Key] = element.Value
		}
		return values, nil
	default:
		return nil, fmt.Errorf("mongodb: result field %q must be a document, got %T", s.metadataField, value)
	}
}
