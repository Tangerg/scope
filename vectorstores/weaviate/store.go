package weaviate

import (
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"

	"github.com/go-openapi/strfmt"
	"github.com/google/uuid"
	"github.com/weaviate/weaviate-go-client/v5/weaviate"
	"github.com/weaviate/weaviate-go-client/v5/weaviate/fault"
	"github.com/weaviate/weaviate-go-client/v5/weaviate/filters"
	"github.com/weaviate/weaviate-go-client/v5/weaviate/graphql"
	"github.com/weaviate/weaviate/entities/models"

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
	Provider = "Weaviate"
)

const (
	fieldContent  = "content"
	fieldMetadata = "metadata"

	additionalID         = "id"
	additionalDistance   = "distance"
	additionalScore      = "score"
	metadataScanPageSize = 256
	storageDataText      = "text"
	contentTokenization  = "word"
)

// DistanceMetric selects the distance function configured on the Weaviate
// collection.
type DistanceMetric string

// The metric is a closed vocabulary because score direction and threshold
// semantics depend on it: the same raw number means "near" under one metric and
// "far" under another, so an unrecognized value must be rejected rather than
// guessed.
const (
	DistanceCosine    DistanceMetric = "cosine"
	DistanceDot       DistanceMetric = "dot"
	DistanceL2Squared DistanceMetric = "l2-squared"
	DistanceHamming   DistanceMetric = "hamming"
	DistanceManhattan DistanceMetric = "manhattan"
)

func (d DistanceMetric) Valid() bool {
	switch d {
	case DistanceCosine, DistanceDot, DistanceL2Squared, DistanceHamming, DistanceManhattan:
		return true
	default:
		return false
	}
}

func (d DistanceMetric) String() string { return string(d) }

func (d DistanceMetric) score(distance float64) vectorstore.Score {
	switch d {
	case DistanceCosine:
		return vectorstore.ScoreFromCosineDistance(distance)
	case DistanceDot:
		return vectorstore.ScoreFromNegativeInnerProductDistance(distance)
	case DistanceL2Squared, DistanceHamming, DistanceManhattan:
		return vectorstore.ScoreFromDistance(distance)
	default:
		return vectorstore.ScoreFromValue(distance)
	}
}

// StoreConfig contains configuration options for Weaviate vector store.
type StoreConfig struct {
	// Client is the Weaviate client instance.
	// Required: must be provided, otherwise initialization will fail.
	Client *weaviate.Client

	// ClassName is the name of the Weaviate class (collection) to use.
	// Required: must be a non-empty string.
	ClassName string

	// InitializeSchema indicates whether to automatically create the class
	// if it does not exist. When set to true, the class will be created
	// with HNSW vector index configuration based on the chosen DistanceMetric.
	// Optional: defaults to false.
	InitializeSchema bool

	// EmbeddingModel is the model used to generate vector embeddings from text.
	// Required: must be provided for both embedding generation and schema initialization.
	EmbeddingModel embedding.Model

	// DocumentBatcher is responsible for batching documents before insertion.
	// Required: must be provided to handle document batching logic.
	DocumentBatcher vectorstore.Batcher

	// DistanceMetric is the distance metric used for the HNSW vector index.
	// Valid values: "cosine" (default), "dot", "l2-squared", "hamming", "manhattan".
	// Optional: defaults to "cosine".
	DistanceMetric DistanceMetric

	// HybridAlpha controls the relative weight of vector evidence in native
	// hybrid search. Nil preserves Weaviate's default; valid values are [0, 1].
	HybridAlpha *float32
}

func (s StoreConfig) Validate() error {
	s.applyDefaults()
	if s.Client == nil {
		return ErrMissingClient
	}
	if s.ClassName == "" {
		return ErrMissingClassName
	}
	if lo.IsNil(s.EmbeddingModel) {
		return ErrMissingEmbeddingModel
	}
	if lo.IsNil(s.DocumentBatcher) {
		return ErrMissingDocumentBatcher
	}
	if !s.DistanceMetric.Valid() {
		return fmt.Errorf("weaviate: unsupported DistanceMetric %q", s.DistanceMetric)
	}
	if s.HybridAlpha != nil && (*s.HybridAlpha < 0 || *s.HybridAlpha > 1) {
		return fmt.Errorf("weaviate: HybridAlpha must be between 0 and 1, got %v", *s.HybridAlpha)
	}
	return nil
}

// applyDefaults fills zero fields with documented defaults.
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

// Store implements the Core vector-store capability interfaces against a Weaviate class. Weaviate
// names properties per class, so the field mapping is fixed at construction
// and cannot vary per request.
type Store struct {
	client           *weaviate.Client
	embeddingClient  embeddingclient.Client
	documentBatcher  vectorstore.Batcher
	className        string
	distanceMetric   DistanceMetric
	hybridAlpha      *float32
	initializeSchema bool
}

// NewStore performs schema setup during construction, which is why it takes
// a context: a store returned before its class schema exists would fail on
// the first index rather than at wiring, where the misconfiguration actually
// is.
func NewStore(ctx context.Context, config StoreConfig) (*Store, error) {
	config.applyDefaults()
	if err := config.Validate(); err != nil {
		return nil, err
	}

	embeddingClient, err := embeddingclient.New(config.EmbeddingModel)
	if err != nil {
		return nil, fmt.Errorf("weaviate: create embedding client: %w", err)
	}

	var hybridAlpha *float32
	if config.HybridAlpha != nil {
		hybridAlpha = new(float32)
		*hybridAlpha = *config.HybridAlpha
	}
	store := &Store{
		client:           config.Client,
		embeddingClient:  embeddingClient,
		documentBatcher:  config.DocumentBatcher,
		className:        config.ClassName,
		distanceMetric:   config.DistanceMetric,
		hybridAlpha:      hybridAlpha,
		initializeSchema: config.InitializeSchema,
	}

	if err = store.initialize(ctx); err != nil {
		return nil, fmt.Errorf("weaviate: initialize vector store: %w", err)
	}

	return store, nil
}

// initialize confirms the class agrees with this store's configuration, and
// creates it when [StoreConfig.InitializeSchema] permits.
//
// The check is not conditional on that flag. InitializeSchema answers "may I
// create a missing class", which is a different question from "is the class I
// found the one I was configured for" — and the second question matters most
// for a class provisioned out of band, which is exactly the case the flag
// turns off. Skipping it there left the configured distance unverified, and a
// wrong distance returns scores that are wrong rather than absent.
func (s *Store) initialize(ctx context.Context) error {
	exists, err := s.client.Schema().ClassExistenceChecker().
		WithClassName(s.className).
		Do(ctx)
	if err != nil {
		return fmt.Errorf("weaviate: check class existence: %w", err)
	}
	if exists {
		return s.checkExistingClass(ctx)
	}
	if !s.initializeSchema {
		return fmt.Errorf("%w: class %s does not exist and InitializeSchema is disabled",
			ErrIncompatibleClass, s.className)
	}

	class := &models.Class{
		Class:           s.className,
		Vectorizer:      "none",
		VectorIndexType: "hnsw",
		VectorIndexConfig: map[string]any{
			"distance": string(s.distanceMetric),
		},
		Properties: []*models.Property{
			{Name: fieldContent, DataType: []string{storageDataText}, Tokenization: contentTokenization},
			{Name: fieldMetadata, DataType: []string{storageDataText}},
		},
	}

	if err = s.client.Schema().ClassCreator().WithClass(class).Do(ctx); err != nil {
		return fmt.Errorf("weaviate: create class %s: %w", s.className, err)
	}

	return s.checkExistingClass(ctx)
}

// The existing collection must preserve the text and JSON fields used by
// retrieval. Metadata filtering reads the original JSON, so native metadata
// property types, tokenizers and null-state indexes cannot change its meaning.
func (s *Store) checkExistingClass(ctx context.Context) error {
	class, err := s.client.Schema().ClassGetter().
		WithClassName(s.className).
		Do(ctx)
	if err != nil {
		return fmt.Errorf("weaviate: get class %s: %w", s.className, err)
	}
	if err := s.compareClassDistance(class); err != nil {
		return err
	}
	for _, name := range []string{fieldContent, fieldMetadata} {
		var property *models.Property
		for _, candidate := range class.Properties {
			if candidate != nil && candidate.Name == name {
				property = candidate
				break
			}
		}
		if property == nil || len(property.DataType) != 1 || property.DataType[0] != storageDataText {
			return fmt.Errorf("%w: class %s requires text property %s", ErrIncompatibleClass, s.className, name)
		}
		if name == fieldContent && ((property.Tokenization != "" && property.Tokenization != contentTokenization) || (property.IndexSearchable != nil && !*property.IndexSearchable)) {
			return fmt.Errorf("%w: class %s content requires searchable word tokenization", ErrIncompatibleClass, s.className)
		}
	}
	return nil
}

// compareClassDistance is the decision checkExistingClass makes, separated from
// the schema fetch so it can be asserted without a live Weaviate.
func (s *Store) compareClassDistance(class *models.Class) error {
	if class == nil {
		return fmt.Errorf("%w: class %s has no schema", ErrIncompatibleClass, s.className)
	}
	config, ok := class.VectorIndexConfig.(map[string]any)
	if !ok {
		return fmt.Errorf("%w: class %s reports vector index config as %T, not an object",
			ErrIncompatibleClass, s.className, class.VectorIndexConfig)
	}
	distance, ok := config["distance"].(string)
	if !ok {
		if _, exists := config["distance"]; exists {
			return fmt.Errorf("%w: class %s has invalid distance %v", ErrIncompatibleClass, s.className, config["distance"])
		}
		// Weaviate omits the key when the class uses its default, which is
		// cosine. Reading the omission as cosine keeps a default-built class
		// usable instead of refusing it for saying nothing.
		distance = string(DistanceCosine)
	}
	if distance != string(s.distanceMetric) {
		return fmt.Errorf("%w: class %s ranks by %s, but this store scores by %s",
			ErrIncompatibleClass, s.className, distance, s.distanceMetric)
	}
	return nil
}

func (s *Store) buildObjects(docs []*document.Document, vectors [][]float64) ([]*models.Object, error) {
	objects := make([]*models.Object, 0, len(docs))

	for i, doc := range docs {
		metaBytes, err := jsonv2.Marshal(doc.Metadata)
		if err != nil {
			return nil, fmt.Errorf("weaviate: marshal metadata for document %s: %w", doc.ID, err)
		}

		properties := map[string]any{
			fieldContent:  doc.Text,
			fieldMetadata: string(metaBytes),
		}
		obj := &models.Object{
			Class:      s.className,
			ID:         strfmt.UUID(doc.ID),
			Vector:     models.C11yVector(embedding.Float32Vector(vectors[i])),
			Properties: properties,
		}
		objects = append(objects, obj)
	}

	return objects, nil
}

func (s *Store) Index(ctx context.Context, request *vectorstore.IndexRequest) (err error) {
	if validateErr := request.Validate(); validateErr != nil {
		return fmt.Errorf("weaviate.Store.Index: %w", validateErr)
	}
	for index, doc := range request.Documents {
		if doc.Media != nil {
			return fmt.Errorf("weaviate.Store.Index: %w: documents[%d] contains unsupported media", vectorstore.ErrInvalidDocument, index)
		}
	}
	for i, doc := range request.Documents {
		if validateObjectIDErr := validateObjectID(doc.ID); validateObjectIDErr != nil {
			return fmt.Errorf("weaviate.Store.Index: documents[%d]: %w", i, validateObjectIDErr)
		}
	}

	var batches []*vectorstore.IndexRequest
	batches, err = request.Batch(ctx, s.documentBatcher)
	if err != nil {
		return fmt.Errorf("weaviate: batch documents: %w", err)
	}

	for _, batch := range batches {
		docs := batch.Documents
		texts, err := batch.Texts()
		if err != nil {
			return fmt.Errorf("vectorstore: project document text: %w", err)
		}
		vectors, err := s.embeddingClient.EmbedTexts(ctx, texts)
		if err != nil {
			return fmt.Errorf("weaviate: embed documents: %w", err)
		}

		objects, err := s.buildObjects(docs, vectors)
		if err != nil {
			return err
		}

		responses, err := s.client.Batch().ObjectsBatcher().
			WithObjects(objects...).
			Do(ctx)
		if err != nil {
			return fmt.Errorf("weaviate: batch insert %d objects to class %s: %w",
				len(objects), s.className, err)
		}

		if err := checkBatchAcknowledgments(objects, responses); err != nil {
			return err
		}
	}

	return nil
}

// checkBatchAcknowledgments requires one successful result per requested ID.
// Weaviate answers a batch whose objects individually failed with a successful
// HTTP call, so the per-object results are the only evidence the batch applied;
// a missing or extra result leaves objects unaccounted for, and a FAILED status
// can arrive without an error payload.
func checkBatchAcknowledgments(objects []*models.Object, responses []models.ObjectsGetResponse) error {
	if len(responses) != len(objects) {
		return fmt.Errorf("weaviate: batch insert returned %d results for %d objects",
			len(responses), len(objects))
	}
	remaining := make(map[strfmt.UUID]struct{}, len(objects))
	for _, object := range objects {
		remaining[object.ID] = struct{}{}
	}
	for index := range responses {
		response := &responses[index]
		result := response.Result
		if result == nil {
			return fmt.Errorf("weaviate: batch insert has no result for object %s", response.ID)
		}
		if result.Errors != nil {
			return fmt.Errorf("weaviate: batch insert error for object %s: %v",
				response.ID, result.Errors.Error)
		}
		if result.Status == nil || *result.Status != models.ObjectsGetResponseAO2ResultStatusSUCCESS {
			return fmt.Errorf("weaviate: batch insert for object %s reported status %s",
				response.ID, lo.FromPtrOr(result.Status, "none"))
		}
		if _, requested := remaining[response.ID]; !requested {
			return fmt.Errorf("weaviate: batch insert acknowledged an unrequested or repeated object %q", response.ID)
		}
		delete(remaining, response.ID)
	}
	return nil
}

func (s *Store) buildNearVector(vector []float64, minScore vectorstore.Score) *graphql.NearVectorArgumentBuilder {
	builder := s.client.GraphQL().NearVectorArgBuilder().
		WithVector(models.C11yVector(embedding.Float32Vector(vector)))

	// WithCertainty is the minimum similarity threshold, only valid for cosine distance.
	if minScore > 0 && s.distanceMetric == DistanceCosine {
		builder = builder.WithCertainty(float32(minScore.Float64()))
	}

	return builder
}

// Search ranks documents with Weaviate's semantic or hybrid search. A metadata
// filter first scans every stored metadata object and selects matching UUIDs;
// the native ranking query receives those UUIDs before applying TopK. Returned
// candidates are rechecked, and a changed predicate result fails the search.
// The scan and ranking are separate requests, not a database snapshot.
func (s *Store) Search(ctx context.Context, request *vectorstore.SearchRequest) (response *vectorstore.SearchResponse, err error) {
	var docs []*vectorstore.SearchResult
	if err = request.Validate(); err != nil {
		return nil, fmt.Errorf("weaviate.Store.Search: %w", err)
	}
	if err = request.Options.RequireMode(vectorstore.SearchModeSemantic, vectorstore.SearchModeHybrid); err != nil {
		return nil, fmt.Errorf("weaviate.Store.Search: %w", err)
	}

	defer func() {
		if err == nil {
			err = response.ValidateFor(request)
		}
	}()

	var selectedIDs []string
	if request.Options.Filter != nil {
		selectedIDs, err = s.matchingIDs(ctx, request.Options.Filter)
		if err != nil {
			return nil, err
		}
		if len(selectedIDs) == 0 {
			return &vectorstore.SearchResponse{}, nil
		}
	}

	vector, err := s.embeddingClient.EmbedText(ctx, request.Query)
	if err != nil {
		return nil, fmt.Errorf("weaviate: embed query: %w", err)
	}

	additionalRelevanceField := additionalDistance
	if request.Options.EffectiveMode() == vectorstore.SearchModeHybrid {
		additionalRelevanceField = additionalScore
	}
	fields := []graphql.Field{
		{Name: fieldContent},
		{Name: fieldMetadata},
		{
			Name: "_additional",
			Fields: []graphql.Field{
				{Name: additionalID},
				{Name: additionalRelevanceField},
			},
		},
	}

	getBuilder := s.client.GraphQL().Get().
		WithClassName(s.className).
		WithFields(fields...).
		WithLimit(request.Options.ResultLimit())
	if request.Options.EffectiveMode() == vectorstore.SearchModeSemantic {
		getBuilder = getBuilder.WithNearVector(s.buildNearVector(vector, request.Options.MinScore))
	} else {
		hybrid := s.client.GraphQL().HybridArgumentBuilder().
			WithQuery(request.Query).
			WithVector(models.C11yVector(embedding.Float32Vector(vector))).
			WithProperties([]string{fieldContent}).
			WithFusionType(graphql.RelativeScore)
		if s.hybridAlpha != nil {
			hybrid = hybrid.WithAlpha(*s.hybridAlpha)
		}
		getBuilder = getBuilder.WithHybrid(hybrid)
	}

	if request.Options.Filter != nil {
		getBuilder = getBuilder.WithWhere(filters.Where().WithPath([]string{additionalID}).WithOperator(filters.ContainsAny).WithValueText(selectedIDs...))
	}

	result, err := getBuilder.Do(ctx)
	if err != nil {
		return nil, fmt.Errorf("weaviate: query class %s: %w", s.className, err)
	}

	docs, err = s.buildDocumentsFromResult(result, request.Options, selectedIDs)
	if err != nil {
		return nil, fmt.Errorf("weaviate: build documents from results: %w", err)
	}

	return &vectorstore.SearchResponse{Results: docs}, nil
}

func (s *Store) resultObjects(result *models.GraphQLResponse) ([]map[string]any, error) {
	if result == nil {
		return nil, errors.New("weaviate: GraphQL response is nil")
	}
	if len(result.Errors) > 0 {
		return nil, fmt.Errorf("weaviate: GraphQL query error: %s", result.Errors[0].Message)
	}
	getData, ok := result.Data["Get"]
	if !ok {
		return nil, errors.New("weaviate: GraphQL response is missing Get data")
	}

	getMap, ok := getData.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("weaviate: GraphQL Get data has type %T, want object", getData)
	}

	classData, ok := getMap[s.className]
	if !ok {
		return nil, fmt.Errorf("weaviate: GraphQL Get data is missing class %q", s.className)
	}

	items, ok := classData.([]any)
	if !ok {
		return nil, fmt.Errorf("weaviate: GraphQL class data has type %T, want array", classData)
	}

	objects := make([]map[string]any, len(items))
	for index, item := range items {
		object, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("weaviate: result object has type %T, want object", item)
		}
		objects[index] = object
	}
	return objects, nil
}

func (s *Store) buildDocumentsFromResult(result *models.GraphQLResponse, options vectorstore.SearchOptions, selectedIDs []string) ([]*vectorstore.SearchResult, error) {
	items, err := s.resultObjects(result)
	if err != nil {
		return nil, err
	}

	docs := make([]*vectorstore.SearchResult, 0, len(items))

	for _, objMap := range items {
		doc := &document.Document{}
		additional, ok := objMap["_additional"].(map[string]any)
		if !ok {
			return nil, errors.New("weaviate: result object is missing _additional")
		}
		id, ok := additional[additionalID].(string)
		if !ok {
			return nil, fmt.Errorf("%w: result object is missing _additional.id", ErrInvalidObjectID)
		}
		if objectIDErr := validateObjectID(id); objectIDErr != nil {
			return nil, fmt.Errorf("weaviate: result object: %w", objectIDErr)
		}
		doc.ID = id
		if options.Filter != nil {
			if _, selected := slices.BinarySearch(selectedIDs, id); !selected {
				return nil, fmt.Errorf("weaviate: returned object %s was not selected by the metadata filter", id)
			}
		}

		content, ok := objMap[fieldContent].(string)
		if !ok || content == "" {
			return nil, fmt.Errorf("weaviate: result object is missing %s", fieldContent)
		}
		doc.Text = content

		doc.Metadata, err = decodeStoredMetadata(objMap[fieldMetadata])
		if err != nil {
			return nil, fmt.Errorf("weaviate: document %s: %w", doc.ID, err)
		}
		if options.Filter != nil {
			values, err := doc.Metadata.Values()
			if err != nil {
				return nil, fmt.Errorf("weaviate: returned object %s metadata: %w", id, err)
			}
			match, err := filter.Match(options.Filter, values)
			if err != nil {
				return nil, fmt.Errorf("weaviate: filter returned object %s: %w", id, err)
			}
			if !match {
				return nil, fmt.Errorf("weaviate: returned object %s no longer matches the metadata filter", id)
			}
		}
		score, err := s.resultScore(additional, options.EffectiveMode())
		if err != nil {
			return nil, err
		}
		if score < options.MinScore {
			continue
		}

		docs = append(docs, &vectorstore.SearchResult{Document: doc, Score: score})
	}

	return docs, nil
}

func (s *Store) resultScore(additional map[string]any, mode vectorstore.SearchMode) (vectorstore.Score, error) {
	if mode == vectorstore.SearchModeSemantic {
		distance, ok := additional[additionalDistance].(float64)
		if !ok {
			return 0, fmt.Errorf("weaviate: result distance has type %T, want number", additional[additionalDistance])
		}
		return s.distanceMetric.score(distance), nil
	}

	raw := additional[additionalScore]
	switch score := raw.(type) {
	case float64:
		return vectorstore.ScoreFromValue(score), nil
	case string:
		value, err := strconv.ParseFloat(score, 64)
		if err != nil {
			return 0, fmt.Errorf("weaviate: parse hybrid result score %q: %w", score, err)
		}
		return vectorstore.ScoreFromValue(value), nil
	default:
		return 0, fmt.Errorf("weaviate: hybrid result score has type %T, want number or string", raw)
	}
}

// DeleteWhere scans all stored metadata with the Core predicate before deleting
// the matching UUIDs. Scan or predicate errors prevent every deletion. Deletes
// are individual requests without revision preconditions; concurrent writes
// require host coordination, and a later deletion error can leave earlier
// deletions applied.
func (s *Store) DeleteWhere(ctx context.Context, predicate filter.Predicate) (err error) {
	if predicate == nil {
		return vectorstore.ErrMissingFilter
	}
	if err = predicate.Validate(); err != nil {
		return fmt.Errorf("weaviate.Store.DeleteWhere: %w", err)
	}

	ids, err := s.matchingIDs(ctx, predicate)
	if err != nil {
		return err
	}
	return s.DeleteIDs(ctx, ids)
}

// matchingIDs completes the cursor scan before ranking or mutation. A short
// page is not a terminal marker; only an empty page establishes exhaustion.
func (s *Store) matchingIDs(ctx context.Context, expr filter.Predicate) ([]string, error) {
	var ids []string
	var after string
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		builder := s.client.GraphQL().Get().WithClassName(s.className).
			WithFields(graphql.Field{Name: fieldMetadata}, graphql.Field{Name: "_additional", Fields: []graphql.Field{{Name: additionalID}}}).
			WithLimit(metadataScanPageSize)
		if after != "" {
			builder = builder.WithAfter(after)
		}
		result, err := builder.Do(ctx)
		if err != nil {
			return nil, fmt.Errorf("weaviate: enumerate class %s: %w", s.className, errors.Join(err, ctx.Err()))
		}
		objects, err := s.resultObjects(result)
		if err != nil {
			return nil, err
		}
		if len(objects) == 0 {
			return ids, nil
		}
		for _, object := range objects {
			additional, ok := object["_additional"].(map[string]any)
			if !ok {
				return nil, errors.New("weaviate: enumerated object is missing _additional")
			}
			id, ok := additional[additionalID].(string)
			if !ok {
				return nil, fmt.Errorf("%w: enumerated object has no UUID", ErrInvalidObjectID)
			}
			if objectIDErr := validateObjectID(id); objectIDErr != nil {
				return nil, fmt.Errorf("weaviate: enumerated object: %w", objectIDErr)
			}
			if id <= after {
				return nil, fmt.Errorf("weaviate: cursor did not advance past %q", after)
			}
			stored, err := decodeStoredMetadata(object[fieldMetadata])
			if err != nil {
				return nil, fmt.Errorf("weaviate: object %s: %w", id, err)
			}
			values, err := stored.Values()
			if err != nil {
				return nil, fmt.Errorf("weaviate: object %s metadata: %w", id, err)
			}
			match, err := filter.Match(expr, values)
			if err != nil {
				return nil, fmt.Errorf("weaviate: filter object %s: %w", id, err)
			}
			if match {
				ids = append(ids, id)
			}
			after = id
		}
	}
}

func decodeStoredMetadata(value any) (metadata.Map, error) {
	encoded, ok := value.(string)
	if !ok || encoded == "" {
		return nil, errors.New("weaviate: missing stored metadata JSON")
	}
	var result metadata.Map
	if err := jsonv2.Unmarshal([]byte(encoded), &result); err != nil {
		return nil, fmt.Errorf("weaviate: decode metadata JSON: %w", err)
	}
	return result, nil
}

// DeleteIDs removes objects by their canonical Weaviate UUIDs. An empty slice is a
// no-op; unknown ids are ignored (idempotent).
func (s *Store) DeleteIDs(ctx context.Context, ids []string) (err error) {
	if len(ids) == 0 {
		return nil
	}
	for i, id := range ids {
		if validateObjectIDErr := validateObjectID(id); validateObjectIDErr != nil {
			return fmt.Errorf("weaviate.Store.DeleteIDs: ids[%d]: %w", i, validateObjectIDErr)
		}
	}

	for _, id := range ids {
		if delErr := s.client.Data().Deleter().
			WithClassName(s.className).
			WithID(id).
			Do(ctx); delErr != nil {
			// A missing object yields a 404; treat unknown ids as a no-op
			// so the operation stays idempotent.
			if clientErr, ok := errors.AsType[*fault.WeaviateClientError](delErr); ok && clientErr.StatusCode == http.StatusNotFound {
				continue
			}
			err = fmt.Errorf("weaviate: delete object %s from class %s: %w",
				id, s.className, delErr)
			return err
		}
	}

	return nil
}

func validateObjectID(id string) error {
	parsed, err := uuid.Parse(id)
	if err != nil || parsed.String() != id {
		return fmt.Errorf("%w %q: must be a lowercase hyphenated UUID", ErrInvalidObjectID, id)
	}
	return nil
}
