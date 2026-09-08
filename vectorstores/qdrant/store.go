package qdrant

import (
	"context"
	"fmt"
	stdmath "math"
	"strconv"

	"github.com/google/uuid"
	"github.com/qdrant/go-client/qdrant"

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
	Provider = "Qdrant"
)

// DistanceMetric identifies the metric configured on the Qdrant collection.
// It is required because Qdrant score direction and threshold semantics depend
// on the collection metric.
type DistanceMetric string

// The metric is a closed vocabulary because score direction and threshold
// semantics depend on it: the same raw number means "near" under one metric and
// "far" under another, so an unrecognized value must be rejected rather than
// guessed.
const (
	DistanceCosine    DistanceMetric = "cosine"
	DistanceDot       DistanceMetric = "dot"
	DistanceEuclid    DistanceMetric = "euclid"
	DistanceManhattan DistanceMetric = "manhattan"
)

func (d DistanceMetric) Valid() bool {
	switch d {
	case DistanceCosine, DistanceDot, DistanceEuclid, DistanceManhattan:
		return true
	default:
		return false
	}
}

func (d DistanceMetric) String() string { return string(d) }

func (d DistanceMetric) qdrant() (qdrant.Distance, error) {
	switch d {
	case DistanceCosine:
		return qdrant.Distance_Cosine, nil
	case DistanceDot:
		return qdrant.Distance_Dot, nil
	case DistanceEuclid:
		return qdrant.Distance_Euclid, nil
	case DistanceManhattan:
		return qdrant.Distance_Manhattan, nil
	default:
		return qdrant.Distance_UnknownDistance, fmt.Errorf("%w, got %q", ErrInvalidDistanceMetric, d)
	}
}

func (d DistanceMetric) score(raw float64) vectorstore.Score {
	switch d {
	case DistanceDot:
		return vectorstore.ScoreFromInnerProduct(raw)
	case DistanceEuclid, DistanceManhattan:
		return vectorstore.ScoreFromDistance(raw)
	case DistanceCosine:
		fallthrough
	default:
		return vectorstore.ScoreFromCosineSimilarity(raw)
	}
}

// rawScoreThreshold converts Scope's normalized minimum score back into the
// collection metric. Qdrant interprets thresholds according to metric
// direction, so Euclidean and Manhattan correctly use a maximum distance.
func (d DistanceMetric) rawScoreThreshold(minScore vectorstore.Score) (float64, bool) {
	value := minScore.Float64()
	if value <= vectorstore.MinRelevanceScore {
		return 0, false
	}

	switch d {
	case DistanceDot:
		if value >= vectorstore.MaxRelevanceScore {
			return stdmath.MaxFloat32, true
		}
		return stdmath.Log(value / (1 - value)), true
	case DistanceEuclid, DistanceManhattan:
		return 1/value - 1, true
	case DistanceCosine:
		fallthrough
	default:
		return 2*value - 1, true
	}
}

const (
	// payloadDocumentContentKey is the payload key for saving document content
	payloadDocumentContentKey = "scope:ai:vectorstore:qdrant:payload_document_content"
)

// StoreConfig contains configuration options for Qdrant vector store.
type StoreConfig struct {
	// Client is the Qdrant client instance for communicating with Qdrant server.
	// Required: must be provided, otherwise initialization will fail.
	Client *qdrant.Client

	// CollectionName is the name of the collection to use for storing vectors.
	// Required: must be a non-empty string.
	CollectionName string

	// DistanceMetric must match the collection's unnamed dense-vector metric.
	// When InitializeSchema is true and the collection does not exist, this
	// metric is used to create it.
	DistanceMetric DistanceMetric

	// Dimensions stays explicit because schema verification must not trigger a
	// hidden, billable embedding request.
	Dimensions int

	// InitializeSchema indicates whether to automatically create the collection
	// if it does not exist. When set to true, the collection will be created
	// with vector configuration based on EmbeddingModel dimensions.
	// Optional: defaults to false.
	InitializeSchema bool

	// EmbeddingModel is the model used to generate vector embeddings from text.
	// It is also used to determine the vector dimension when creating collections.
	// Required: must be provided for both embedding generation and schema initialization.
	EmbeddingModel embedding.Model

	// DocumentBatcher is responsible for batching documents before insertion.
	// This helps optimize bulk operations and embedding generation.
	// Required: must be provided to handle document batching logic.
	DocumentBatcher vectorstore.Batcher
}

func (s StoreConfig) Validate() error {
	if s.Client == nil {
		return ErrMissingClient
	}
	if s.CollectionName == "" {
		return ErrMissingCollectionName
	}
	if _, err := s.DistanceMetric.qdrant(); err != nil {
		return err
	}
	if lo.IsNil(s.EmbeddingModel) {
		return ErrMissingEmbeddingModel
	}
	if lo.IsNil(s.DocumentBatcher) {
		return ErrMissingDocumentBatcher
	}
	if s.InitializeSchema && s.Dimensions <= 0 {
		return fmt.Errorf("qdrant: Dimensions must be > 0 when InitializeSchema is enabled")
	}
	return nil
}

var (
	_ vectorstore.Indexer       = (*Store)(nil)
	_ vectorstore.Searcher      = (*Store)(nil)
	_ vectorstore.FilterDeleter = (*Store)(nil)
	_ vectorstore.IDDeleter     = (*Store)(nil)
)

// Store implements [vectorstore.Store] against a Qdrant collection. The
// distance metric is held here because Qdrant's score direction and threshold
// semantics depend on the metric the collection was created with.
type Store struct {
	client           *qdrant.Client
	embeddingClient  embeddingclient.Client
	documentBatcher  vectorstore.Batcher
	collectionName   string
	distanceMetric   DistanceMetric
	dimensions       int
	initializeSchema bool
}

// NewStore performs schema setup during construction, which is why it takes a
// context: a store returned before its collection exists would fail on the
// first index rather than at wiring, where the misconfiguration actually is.
func NewStore(ctx context.Context, config StoreConfig) (*Store, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}

	embeddingClient, err := embeddingclient.New(config.EmbeddingModel)
	if err != nil {
		return nil, fmt.Errorf("qdrant: create embedding client: %w", err)
	}

	store := &Store{
		client:           config.Client,
		embeddingClient:  embeddingClient,
		documentBatcher:  config.DocumentBatcher,
		collectionName:   config.CollectionName,
		distanceMetric:   config.DistanceMetric,
		dimensions:       config.Dimensions,
		initializeSchema: config.InitializeSchema,
	}

	if err = store.initialize(ctx); err != nil {
		return nil, fmt.Errorf("qdrant: initialize vector store: %w", err)
	}

	return store, nil
}

func (s *Store) initialize(ctx context.Context) error {
	if !s.initializeSchema {
		return nil
	}

	exists, err := s.client.CollectionExists(ctx, s.collectionName)
	if err != nil {
		return fmt.Errorf("qdrant: check collection existence: %w", err)
	}

	distance, err := s.distanceMetric.qdrant()
	if err != nil {
		return err
	}
	if exists {
		info, getCollectionInfoErr := s.client.GetCollectionInfo(ctx, s.collectionName)
		if getCollectionInfoErr != nil {
			return fmt.Errorf("qdrant: inspect existing collection %s: %w", s.collectionName, getCollectionInfoErr)
		}
		if getCollectionInfoErr = validateCollectionSchema(info, s.dimensions, distance); getCollectionInfoErr != nil {
			return fmt.Errorf("qdrant: collection %s: %w", s.collectionName, getCollectionInfoErr)
		}
		return nil
	}

	err = s.client.CreateCollection(ctx, &qdrant.CreateCollection{
		CollectionName: s.collectionName,
		VectorsConfig: qdrant.NewVectorsConfig(&qdrant.VectorParams{
			Size:     uint64(s.dimensions),
			Distance: distance,
		}),
	})
	if err != nil {
		return fmt.Errorf("qdrant: create collection %s: %w", s.collectionName, err)
	}

	return nil
}

func validateCollectionSchema(info *qdrant.CollectionInfo, dimensions int, distance qdrant.Distance) error {
	if dimensions <= 0 {
		return fmt.Errorf("%w: embedding dimensions must be positive, got %d", ErrIncompatibleCollection, dimensions)
	}

	vectors := info.GetConfig().GetParams().GetVectorsConfig()
	params := vectors.GetParams()
	if params == nil {
		if vectors.GetParamsMap() != nil {
			return fmt.Errorf("%w: named vectors are not supported", ErrIncompatibleCollection)
		}
		return fmt.Errorf("%w: unnamed dense-vector configuration is missing", ErrIncompatibleCollection)
	}
	if params.GetSize() != uint64(dimensions) {
		return fmt.Errorf("%w: vector dimensions are %d, embedding model produces %d",
			ErrIncompatibleCollection, params.GetSize(), dimensions)
	}
	if params.GetDistance() != distance {
		return fmt.Errorf("%w: distance metric is %s, configured metric requires %s",
			ErrIncompatibleCollection, params.GetDistance(), distance)
	}
	return nil
}

// buildDeletePoints waits for the deletion to be applied. Qdrant otherwise
// acknowledges an update once it reaches the write-ahead log, which would let a
// Search issued after a successful delete still return the removed points.
func (s *Store) buildDeletePoints(selector *qdrant.PointsSelector) *qdrant.DeletePoints {
	return &qdrant.DeletePoints{
		CollectionName: s.collectionName,
		Wait:           new(true),
		Points:         selector,
	}
}

func (s *Store) buildUpsertPoints(ctx context.Context, request *vectorstore.IndexRequest) (*qdrant.UpsertPoints, error) {
	upsertPoints := &qdrant.UpsertPoints{
		CollectionName: s.collectionName,
		Wait:           new(true),
	}

	batches, err := request.Batch(ctx, s.documentBatcher)
	if err != nil {
		return nil, fmt.Errorf("qdrant: batch documents: %w", err)
	}

	for _, batch := range batches {
		docs := batch.Documents
		texts, err := batch.Texts()
		if err != nil {
			return nil, fmt.Errorf("qdrant: project document text: %w", err)
		}
		vectors, err := s.embeddingClient.EmbedTexts(ctx, texts)
		if err != nil {
			return nil, fmt.Errorf("qdrant: embed documents: %w", err)
		}

		for i, doc := range docs {
			point, err := s.buildPointStruct(doc, vectors[i])
			if err != nil {
				return nil, fmt.Errorf("qdrant: build point for document %s: %w", doc.ID, err)
			}

			upsertPoints.Points = append(upsertPoints.Points, point)
		}
	}

	return upsertPoints, nil
}

func (s *Store) buildPointStruct(doc *document.Document, vector []float64) (*qdrant.PointStruct, error) {
	id, err := parsePointID(doc.ID)
	if err != nil {
		return nil, err
	}

	point := &qdrant.PointStruct{
		Id:      id,
		Vectors: qdrant.NewVectors(embedding.Float32Vector(vector)...),
	}

	metadataValues, err := doc.Metadata.Values()
	if err != nil {
		return nil, fmt.Errorf("qdrant: decode metadata: %w", err)
	}
	payload, err := qdrant.TryValueMap(metadataValues)
	if err != nil {
		return nil, fmt.Errorf("qdrant: convert metadata to payload: %w", err)
	}
	point.Payload = payload

	contentValue, err := qdrant.NewValue(doc.Text)
	if err != nil {
		return nil, fmt.Errorf("qdrant: create content value: %w", err)
	}
	point.Payload[payloadDocumentContentKey] = contentValue

	return point, nil
}

func (s *Store) Index(ctx context.Context, request *vectorstore.IndexRequest) (err error) {
	if validateErr := request.Validate(); validateErr != nil {
		return fmt.Errorf("qdrant.Store.Index: %w", validateErr)
	}
	docs := request.Documents
	for _, doc := range docs {
		if _, parsePointIDErr := parsePointID(doc.ID); parsePointIDErr != nil {
			return fmt.Errorf("qdrant.Store.Index: %w", parsePointIDErr)
		}
	}

	var upsertPoints *qdrant.UpsertPoints
	upsertPoints, err = s.buildUpsertPoints(ctx, request)
	if err != nil {
		return err
	}

	_, err = s.client.Upsert(ctx, upsertPoints)
	if err != nil {
		return fmt.Errorf("qdrant: upsert %d points to collection %s: %w",
			len(upsertPoints.Points), s.collectionName, err)
	}

	return nil
}

func (s *Store) buildQueryPoints(ctx context.Context, req *vectorstore.SearchRequest) (*qdrant.QueryPoints, error) {
	queryPoints := &qdrant.QueryPoints{
		CollectionName: s.collectionName,
		Limit:          new(uint64(req.Options.ResultLimit())),
		WithPayload:    qdrant.NewWithPayload(true),
	}
	if threshold, ok := s.distanceMetric.rawScoreThreshold(req.Options.MinScore); ok {
		threshold32 := float32(max(-stdmath.MaxFloat32, min(stdmath.MaxFloat32, threshold)))
		queryPoints.ScoreThreshold = &threshold32
	}

	if req.Options.Filter != nil {
		visitor := newVisitor()
		if err := req.Options.Filter.Accept(visitor); err != nil {
			return nil, fmt.Errorf("qdrant: convert filter: %w", err)
		}
		queryPoints.Filter = visitor.snapshot()
	}

	vector, err := s.embeddingClient.EmbedText(ctx, req.Query)
	if err != nil {
		return nil, fmt.Errorf("qdrant: embed query: %w", err)
	}

	queryPoints.Query = qdrant.NewQuery(embedding.Float32Vector(vector)...)

	return queryPoints, nil
}

func (s *Store) convertQdrantValue(value *qdrant.Value) any {
	if value == nil {
		return nil
	}

	switch kind := value.Kind.(type) {
	case *qdrant.Value_DoubleValue:
		return kind.DoubleValue
	case *qdrant.Value_IntegerValue:
		return kind.IntegerValue
	case *qdrant.Value_StringValue:
		return kind.StringValue
	case *qdrant.Value_BoolValue:
		return kind.BoolValue
	case *qdrant.Value_NullValue:
		return nil
	case *qdrant.Value_StructValue:
		return s.convertQdrantStruct(kind.StructValue)
	case *qdrant.Value_ListValue:
		return s.convertQdrantList(kind.ListValue)
	default:
		return nil
	}
}

func (s *Store) convertQdrantStruct(qs *qdrant.Struct) map[string]any {
	if qs == nil || qs.Fields == nil {
		return nil
	}

	result := make(map[string]any, len(qs.Fields))
	for key, val := range qs.Fields {
		result[key] = s.convertQdrantValue(val)
	}

	return result
}

func (s *Store) convertQdrantList(l *qdrant.ListValue) []any {
	if l == nil || len(l.Values) == 0 {
		return nil
	}

	result := make([]any, len(l.Values))
	for i, val := range l.Values {
		result[i] = s.convertQdrantValue(val)
	}

	return result
}

func (s *Store) convertPayloadToMetadata(payload map[string]*qdrant.Value) map[string]any {
	if payload == nil {
		return nil
	}

	metadata := make(map[string]any, len(payload))
	for key, value := range payload {
		if key == payloadDocumentContentKey || value == nil {
			continue
		}
		metadata[key] = s.convertQdrantValue(value)
	}

	return metadata
}

func (s *Store) buildDocumentsFromPoints(scoredPoints []*qdrant.ScoredPoint) ([]*vectorstore.SearchResult, error) {
	docs := make([]*vectorstore.SearchResult, 0, len(scoredPoints))

	for i, point := range scoredPoints {
		if point == nil {
			return nil, fmt.Errorf("qdrant: query result %d is nil", i)
		}
		id, err := formatPointID(point.GetId())
		if err != nil {
			return nil, fmt.Errorf("qdrant: query result %d: %w", i, err)
		}
		payload := point.GetPayload()
		contentValue, ok := payload[payloadDocumentContentKey]
		if !ok || contentValue == nil || contentValue.GetStringValue() == "" {
			return nil, fmt.Errorf("qdrant: query result %d is missing document text", i)
		}

		doc := &document.Document{ID: id, Text: contentValue.GetStringValue()}
		doc.Metadata, err = metadata.FromValues(s.convertPayloadToMetadata(payload))
		if err != nil {
			return nil, fmt.Errorf("qdrant: decode metadata for query result %d: %w", i, err)
		}

		docs = append(docs, &vectorstore.SearchResult{
			Document: doc,
			Score:    s.distanceMetric.score(float64(point.GetScore())),
		})
	}

	return docs, nil
}

func (s *Store) Search(ctx context.Context, req *vectorstore.SearchRequest) (response *vectorstore.SearchResponse, err error) {
	var docs []*vectorstore.SearchResult
	if err = req.Validate(); err != nil {
		return nil, fmt.Errorf("qdrant.Store.Search: %w", err)
	}
	if err = req.Options.RequireMode(vectorstore.SearchModeSemantic); err != nil {
		return nil, fmt.Errorf("qdrant.Store.Search: %w", err)
	}

	defer func() {
		if err == nil {
			err = response.ValidateFor(req)
		}
	}()

	var queryPoints *qdrant.QueryPoints
	queryPoints, err = s.buildQueryPoints(ctx, req)
	if err != nil {
		return nil, err
	}

	var scoredPoints []*qdrant.ScoredPoint
	scoredPoints, err = s.client.Query(ctx, queryPoints)
	if err != nil {
		return nil, fmt.Errorf("qdrant: query collection %s: %w", s.collectionName, err)
	}

	docs, err = s.buildDocumentsFromPoints(scoredPoints)
	if err != nil {
		return nil, fmt.Errorf("qdrant: build documents from query results: %w", err)
	}

	return &vectorstore.SearchResponse{Results: docs}, nil
}

// DeleteWhere removes every point matching expr. The request waits for the
// deletion to be applied, because Qdrant otherwise answers as soon as the
// operation reaches the write-ahead log and a following Search would still
// return the removed points. Implements [vectorstore.FilterDeleter].
func (s *Store) DeleteWhere(ctx context.Context, expr filter.Predicate) (err error) {
	if expr == nil {
		return vectorstore.ErrMissingFilter
	}
	if err = expr.Validate(); err != nil {
		return fmt.Errorf("qdrant.Store.DeleteWhere: %w", err)
	}

	visitor := newVisitor()
	if err = expr.Accept(visitor); err != nil {
		return fmt.Errorf("qdrant: convert filter: %w", err)
	}

	_, err = s.client.Delete(ctx, s.buildDeletePoints(qdrant.NewPointsSelectorFilter(visitor.snapshot())))
	if err != nil {
		return fmt.Errorf("qdrant: delete points from collection %s: %w", s.collectionName, err)
	}

	return nil
}

// DeleteIDs removes points by their canonical uint64 or UUID identifiers. An
// empty slice is a no-op; unknown ids are ignored (idempotent). Like
// DeleteWhere, the request waits for the deletion to be applied.
func (s *Store) DeleteIDs(ctx context.Context, ids []string) (err error) {
	if len(ids) == 0 {
		return nil
	}

	pointIDs := make([]*qdrant.PointId, len(ids))
	for i, id := range ids {
		pointIDs[i], err = parsePointID(id)
		if err != nil {
			return fmt.Errorf("qdrant.Store.DeleteIDs: ids[%d]: %w", i, err)
		}
	}

	_, err = s.client.Delete(ctx, s.buildDeletePoints(qdrant.NewPointsSelector(pointIDs...)))
	if err != nil {
		return fmt.Errorf("qdrant: delete points by ids from collection %s: %w", s.collectionName, err)
	}

	return nil
}

func parsePointID(id string) (*qdrant.PointId, error) {
	if number, err := strconv.ParseUint(id, 10, 64); err == nil && strconv.FormatUint(number, 10) == id {
		return qdrant.NewIDNum(number), nil
	}
	if err := uuid.Validate(id); err != nil {
		return nil, fmt.Errorf("%w %q: must be a canonical uint64 or UUID", ErrInvalidPointID, id)
	}
	return qdrant.NewID(id), nil
}

func formatPointID(id *qdrant.PointId) (string, error) {
	if id == nil {
		return "", fmt.Errorf("%w: query result has no point ID", ErrInvalidPointID)
	}
	switch value := id.GetPointIdOptions().(type) {
	case *qdrant.PointId_Num:
		return strconv.FormatUint(value.Num, 10), nil
	case *qdrant.PointId_Uuid:
		if err := uuid.Validate(value.Uuid); err != nil {
			return "", fmt.Errorf("%w %q: query result UUID is invalid", ErrInvalidPointID, value.Uuid)
		}
		return value.Uuid, nil
	default:
		return "", fmt.Errorf("%w: query result uses an unsupported point ID", ErrInvalidPointID)
	}
}
