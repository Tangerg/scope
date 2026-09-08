package weaviate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"

	"github.com/go-openapi/strfmt"
	"github.com/google/uuid"
	"github.com/weaviate/weaviate-go-client/v5/weaviate"
	"github.com/weaviate/weaviate-go-client/v5/weaviate/fault"
	"github.com/weaviate/weaviate-go-client/v5/weaviate/graphql"
	"github.com/weaviate/weaviate/entities/models"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/embeddingclient"
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

	additionalID       = "id"
	additionalDistance = "distance"
	additionalScore    = "score"
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

// MetadataProperty declares one metadata key as a class property so filters
// can select on it.
//
// Weaviate classes are typed and a where filter may only name a declared
// property, so the filterable keys have to be known when the class is created
// — the same constraint cassandra, milvus, and mongodb answer with their own
// declarations.
type MetadataProperty struct {
	// Name is the metadata key, used verbatim as the property name.
	Name string

	// DataType is the Weaviate data type: "text", "int", "number",
	// "boolean", or "date".
	DataType string
}

// MetadataDataText is the data type whose tokenization the store pins. Filters
// compare whole values case-sensitively, and Weaviate's default word
// tokenization "splits text by any non-alphanumeric characters, then
// lowercases each token" — field tokenization instead "treats the entire value
// of the property as a single token" and "preserves both case and symbols".
const MetadataDataText = "text"

// metadataTextTokenization keeps a declared text property exactly comparable.
const metadataTextTokenization = "field"

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

	// MetadataProperties enumerates the metadata keys filters may select on.
	// Each becomes a class property under InitializeSchema and is written
	// alongside the document. A filter naming any other key is rejected,
	// because a where filter on an undeclared property is not a narrower
	// query — Weaviate has no such property to compare.
	MetadataProperties []MetadataProperty
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
	return s.validateMetadataProperties()
}

func (s StoreConfig) validateMetadataProperties() error {
	seen := make(map[string]struct{}, len(s.MetadataProperties))
	for index, property := range s.MetadataProperties {
		if property.Name == "" {
			return fmt.Errorf("weaviate: MetadataProperties[%d].Name must not be empty", index)
		}
		if property.Name == fieldContent || property.Name == fieldMetadata {
			return fmt.Errorf("weaviate: MetadataProperties[%d] uses reserved property %q",
				index, property.Name)
		}
		if _, duplicate := seen[property.Name]; duplicate {
			return fmt.Errorf("weaviate: MetadataProperties[%d] duplicates %q", index, property.Name)
		}
		seen[property.Name] = struct{}{}
		switch property.DataType {
		case MetadataDataText, "int", "number", "boolean", "date":
		default:
			return fmt.Errorf("weaviate: MetadataProperties[%d] has unsupported DataType %q",
				index, property.DataType)
		}
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

// Store implements [vectorstore.Store] against a Weaviate class. Weaviate
// names properties per class, so the field mapping is fixed at construction
// and cannot vary per request.
type Store struct {
	client             *weaviate.Client
	embeddingClient    embeddingclient.Client
	documentBatcher    vectorstore.Batcher
	className          string
	metadataProperties []MetadataProperty
	distanceMetric     DistanceMetric
	hybridAlpha        *float32
	initializeSchema   bool
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
		client:             config.Client,
		embeddingClient:    embeddingClient,
		documentBatcher:    config.DocumentBatcher,
		className:          config.ClassName,
		metadataProperties: slices.Clone(config.MetadataProperties),
		distanceMetric:     config.DistanceMetric,
		hybridAlpha:        hybridAlpha,
		initializeSchema:   config.InitializeSchema,
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
		Properties: s.classProperties(),
	}

	if err = s.client.Schema().ClassCreator().WithClass(class).Do(ctx); err != nil {
		return fmt.Errorf("weaviate: create class %s: %w", s.className, err)
	}

	return nil
}

// classProperties declares the storage fields plus every filterable metadata
// key. A declared text property pins field tokenization so a filter compares
// the whole value case-sensitively; content keeps Weaviate's default word
// tokenization, which is what hybrid search needs.
// checkExistingClass verifies that a class this store did not create ranks by
// the distance this store scores against.
//
// Existence is not agreement. Search converts Weaviate's distance into a Score
// using the metric from this store's own config, so a class built with l2
// squared while the config says cosine returns scores that are wrong rather
// than missing: nothing fails, the ranking is silently mis-scaled. Returning
// early because the class was already there accepted exactly that.
//
// Only the distance is checked. Weaviate stores no vector width on a class
// whose vectorizer is none — the length comes with each object — so there is
// no declared dimension here to disagree with.
func (s *Store) checkExistingClass(ctx context.Context) error {
	class, err := s.client.Schema().ClassGetter().
		WithClassName(s.className).
		Do(ctx)
	if err != nil {
		return fmt.Errorf("weaviate: get class %s: %w", s.className, err)
	}
	return s.compareClassDistance(class)
}

// compareClassDistance is the decision checkExistingClass makes, separated from
// the schema fetch so it can be asserted without a live Weaviate.
func (s *Store) compareClassDistance(class *models.Class) error {
	config, ok := class.VectorIndexConfig.(map[string]any)
	if !ok {
		return fmt.Errorf("%w: class %s reports vector index config as %T, not an object",
			ErrIncompatibleClass, s.className, class.VectorIndexConfig)
	}
	distance, ok := config["distance"].(string)
	if !ok {
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

func (s *Store) classProperties() []*models.Property {
	properties := []*models.Property{
		{Name: fieldContent, DataType: []string{"text"}},
		{Name: fieldMetadata, DataType: []string{"text"}},
	}
	for _, declared := range s.metadataProperties {
		property := &models.Property{
			Name:     declared.Name,
			DataType: []string{declared.DataType},
		}
		if declared.DataType == MetadataDataText {
			property.Tokenization = metadataTextTokenization
		}
		properties = append(properties, property)
	}
	return properties
}

func (s *Store) buildObjects(docs []*document.Document, vectors [][]float64) ([]*models.Object, error) {
	objects := make([]*models.Object, 0, len(docs))

	for i, doc := range docs {
		metaBytes, err := json.Marshal(doc.Metadata)
		if err != nil {
			return nil, fmt.Errorf("weaviate: marshal metadata for document %s: %w", doc.ID, err)
		}

		properties := map[string]any{
			fieldContent:  doc.Text,
			fieldMetadata: string(metaBytes),
		}
		// The blob round-trips every key losslessly; a declared key is
		// written again as its own property because that is the only shape a
		// where filter can select on.
		if err := s.addDeclaredProperties(properties, doc); err != nil {
			return nil, err
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

// addDeclaredProperties copies each declared metadata key onto the object. A
// key the document does not carry is left unset, which Weaviate reads as null
// and IsNull matches — the same answer filter.Match gives for an absent key.
func (s *Store) addDeclaredProperties(properties map[string]any, doc *document.Document) error {
	for _, declared := range s.metadataProperties {
		value, present, err := doc.Metadata.Decode[any](declared.Name)
		if err != nil {
			return fmt.Errorf("weaviate: decode metadata %q for document %s: %w",
				declared.Name, doc.ID, err)
		}
		if present && value != nil {
			properties[declared.Name] = value
		}
	}
	return nil
}

func (s *Store) Index(ctx context.Context, request *vectorstore.IndexRequest) (err error) {
	if validateErr := request.Validate(); validateErr != nil {
		return fmt.Errorf("weaviate.Store.Index: %w", validateErr)
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

// checkBatchAcknowledgments requires one successful result per object sent.
// Weaviate answers a batch whose objects individually failed with a successful
// HTTP call, so the per-object results are the only evidence the batch applied;
// a missing or extra result leaves objects unaccounted for, and a FAILED status
// can arrive without an error payload.
func checkBatchAcknowledgments(objects []*models.Object, responses []models.ObjectsGetResponse) error {
	if len(responses) != len(objects) {
		return fmt.Errorf("weaviate: batch insert returned %d results for %d objects",
			len(responses), len(objects))
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

func (s *Store) Search(ctx context.Context, req *vectorstore.SearchRequest) (response *vectorstore.SearchResponse, err error) {
	var docs []*vectorstore.SearchResult
	if err = req.Validate(); err != nil {
		return nil, fmt.Errorf("weaviate.Store.Search: %w", err)
	}
	if err = req.Options.RequireMode(vectorstore.SearchModeSemantic, vectorstore.SearchModeHybrid); err != nil {
		return nil, fmt.Errorf("weaviate.Store.Search: %w", err)
	}

	defer func() {
		if err == nil {
			err = response.ValidateFor(req)
		}
	}()

	vector, err := s.embeddingClient.EmbedText(ctx, req.Query)
	if err != nil {
		return nil, fmt.Errorf("weaviate: embed query: %w", err)
	}

	additionalRelevanceField := additionalDistance
	if req.Options.EffectiveMode() == vectorstore.SearchModeHybrid {
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
		WithLimit(req.Options.ResultLimit())
	if req.Options.EffectiveMode() == vectorstore.SearchModeSemantic {
		getBuilder = getBuilder.WithNearVector(s.buildNearVector(vector, req.Options.MinScore))
	} else {
		hybrid := s.client.GraphQL().HybridArgumentBuilder().
			WithQuery(req.Query).
			WithVector(models.C11yVector(embedding.Float32Vector(vector))).
			WithProperties([]string{fieldContent}).
			WithFusionType(graphql.RelativeScore)
		if s.hybridAlpha != nil {
			hybrid = hybrid.WithAlpha(*s.hybridAlpha)
		}
		getBuilder = getBuilder.WithHybrid(hybrid)
	}

	if req.Options.Filter != nil {
		visitor := newVisitor(s.metadataProperties)
		if acceptErr := req.Options.Filter.Accept(visitor); acceptErr != nil {
			return nil, fmt.Errorf("weaviate: convert filter: %w", acceptErr)
		}
		getBuilder = getBuilder.WithWhere(visitor.snapshot())
	}

	result, err := getBuilder.Do(ctx)
	if err != nil {
		return nil, fmt.Errorf("weaviate: query class %s: %w", s.className, err)
	}

	if len(result.Errors) > 0 {
		return nil, fmt.Errorf("weaviate: GraphQL query error: %v", result.Errors[0].Message)
	}

	docs, err = s.buildDocumentsFromResult(result, req.Options)
	if err != nil {
		return nil, fmt.Errorf("weaviate: build documents from results: %w", err)
	}

	return &vectorstore.SearchResponse{Results: docs}, nil
}

func (s *Store) buildDocumentsFromResult(
	result *models.GraphQLResponse,
	options vectorstore.SearchOptions,
) ([]*vectorstore.SearchResult, error) {
	if result == nil {
		return nil, errors.New("weaviate: GraphQL response is nil")
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

	docs := make([]*vectorstore.SearchResult, 0, len(items))

	for _, item := range items {
		objMap, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("weaviate: result object has type %T, want object", item)
		}

		doc := &document.Document{}
		additional, ok := objMap["_additional"].(map[string]any)
		if !ok {
			return nil, errors.New("weaviate: result object is missing _additional")
		}
		id, ok := additional[additionalID].(string)
		if !ok || id == "" {
			return nil, errors.New("weaviate: result object is missing _additional.id")
		}
		doc.ID = id
		score, err := s.resultScore(additional, options.EffectiveMode())
		if err != nil {
			return nil, err
		}
		if score < options.MinScore {
			continue
		}

		content, ok := objMap[fieldContent].(string)
		if !ok || content == "" {
			return nil, fmt.Errorf("weaviate: result object is missing %s", fieldContent)
		}
		doc.Text = content

		if metaStr, ok := objMap[fieldMetadata].(string); ok && metaStr != "" && metaStr != "null" {
			if err := json.Unmarshal([]byte(metaStr), &doc.Metadata); err != nil {
				return nil, fmt.Errorf("weaviate: decode metadata: %w", err)
			}
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

func (s *Store) DeleteWhere(ctx context.Context, expr filter.Predicate) (err error) {
	if expr == nil {
		return vectorstore.ErrMissingFilter
	}
	if err = expr.Validate(); err != nil {
		return fmt.Errorf("weaviate.Store.DeleteWhere: %w", err)
	}

	visitor := newVisitor(s.metadataProperties)
	if err = expr.Accept(visitor); err != nil {
		return fmt.Errorf("weaviate: convert filter: %w", err)
	}

	// One batch delete removes at most QUERY_MAXIMUM_RESULTS objects — the
	// response calls Successful the count "in this round" — and Weaviate's
	// guidance for a filter that matches more is to re-run the query. A single
	// call would report success after deleting the first 10,000 of them.
	where := visitor.snapshot()
	for {
		result, deleteErr := s.client.Batch().ObjectsBatchDeleter().
			WithClassName(s.className).
			WithWhere(where).
			Do(ctx)
		if deleteErr != nil {
			return fmt.Errorf("weaviate: delete from class %s: %w", s.className, deleteErr)
		}
		round, roundErr := batchDeleteRound(s.className, result)
		if roundErr != nil {
			return roundErr
		}
		if round.remaining() {
			continue
		}
		return nil
	}
}

// deleteRound is one batch delete's accounting.
type deleteRound struct {
	matches    int64
	successful int64
}

// remaining reports whether the filter still selects objects this round did not
// reach, which happens when it matched the server's per-query maximum.
func (d deleteRound) remaining() bool { return d.matches > d.successful }

// batchDeleteRound reads what one batch delete actually did.
//
// Objects that "should have been deleted but could not be" are reported in
// Failed rather than as a call error, so a nil error alone does not mean the
// round removed what it matched. A round that matched more than it deleted has
// hit the per-query maximum; a round that matched more and deleted nothing is
// not making progress, and re-running it would loop forever.
func batchDeleteRound(className string, response *models.BatchDeleteResponse) (deleteRound, error) {
	if response == nil || response.Results == nil {
		return deleteRound{}, fmt.Errorf("weaviate: batch delete for class %s returned no results", className)
	}
	results := response.Results
	if results.Failed != 0 {
		return deleteRound{}, fmt.Errorf("weaviate: batch delete for class %s failed on %d of %d matched objects",
			className, results.Failed, results.Matches)
	}
	round := deleteRound{matches: results.Matches, successful: results.Successful}
	if round.remaining() && results.Successful == 0 {
		return deleteRound{}, fmt.Errorf(
			"weaviate: batch delete for class %s matched %d objects and deleted none, so repeating cannot progress",
			className, results.Matches)
	}
	return round, nil
}

// DeleteIDs removes objects by their Weaviate UUIDs. An empty slice is a
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
	if err := uuid.Validate(id); err != nil {
		return fmt.Errorf("%w %q: must be a UUID", ErrInvalidObjectID, id)
	}
	return nil
}
