package typesense

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/samber/lo"
	"github.com/typesense/typesense-go/v3/typesense"
	"github.com/typesense/typesense-go/v3/typesense/api"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/embeddingclient"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

// Provider is the stable backend name for host-side attribution.
const Provider = "Typesense"

var collectionNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Exported defaults keep constructor behavior visible and overridable.
const (
	DefaultCollectionName = "scope_vector_store"
	idField               = "doc_id"
	contentField          = "content"
	metadataField         = "metadata"
	embeddingField        = "embedding"
)

// StoreConfig contains configuration options for the Typesense vector
// store.
type StoreConfig struct {
	// Client is the typesense-go client. Required.
	Client *typesense.Client

	// CollectionName names the Typesense collection. Optional:
	// defaults to [DefaultCollectionName].
	CollectionName string

	// EmbeddingModel produces vectors for the documents. Required.
	EmbeddingModel embedding.Model

	// DocumentBatcher batches documents before upsert. Required.
	DocumentBatcher vectorstore.Batcher

	// Dimensions sets the vector width for a new collection, and is required
	// when InitializeSchema is true: the width is part of the field definition,
	// and nothing here can read it off a collection that does not exist yet.
	Dimensions int

	// InitializeSchema, when true, creates the collection with the
	// right schema if it doesn't already exist.
	InitializeSchema bool

	// HybridAlpha controls the vector weight in Typesense's native hybrid
	// fusion. Nil preserves the provider default; valid values are [0, 1].
	HybridAlpha *float32
}

func (s StoreConfig) Validate() error {
	s.applyDefaults()
	if s.Client == nil {
		return errors.New("typesense: Client is required")
	}
	if lo.IsNil(s.EmbeddingModel) {
		return errors.New("typesense: EmbeddingModel is required")
	}
	if lo.IsNil(s.DocumentBatcher) {
		return errors.New("typesense: DocumentBatcher is required")
	}
	if s.Dimensions < 0 {
		return errors.New("typesense: Dimensions must be >= 0")
	}
	if !collectionNamePattern.MatchString(s.CollectionName) {
		return fmt.Errorf("typesense: CollectionName=%q must be a safe identifier", s.CollectionName)
	}
	if s.HybridAlpha != nil && (*s.HybridAlpha < 0 || *s.HybridAlpha > 1) {
		return fmt.Errorf("typesense: HybridAlpha must be between 0 and 1, got %v", *s.HybridAlpha)
	}
	return nil
}

// applyDefaults fills zero fields with documented defaults.
func (s *StoreConfig) applyDefaults() {
	s.CollectionName = cmp.Or(s.CollectionName, DefaultCollectionName)
}

var (
	_ vectorstore.Indexer       = (*Store)(nil)
	_ vectorstore.Searcher      = (*Store)(nil)
	_ vectorstore.FilterDeleter = (*Store)(nil)
)

// Store implements vector-store capabilities with Typesense.
type Store struct {
	client          *typesense.Client
	collectionName  string
	embeddingClient embeddingclient.Client
	documentBatcher vectorstore.Batcher
	dimensions      int
	hybridAlpha     *float32
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
		return nil, fmt.Errorf("typesense: create embedding client: %w", err)
	}

	var hybridAlpha *float32
	if config.HybridAlpha != nil {
		hybridAlpha = new(float32)
		*hybridAlpha = *config.HybridAlpha
	}
	store := &Store{
		client:          config.Client,
		collectionName:  config.CollectionName,
		embeddingClient: embeddingClient,
		documentBatcher: config.DocumentBatcher,
		dimensions:      config.Dimensions,
		hybridAlpha:     hybridAlpha,
	}

	if err = store.initialize(ctx, config.InitializeSchema); err != nil {
		return nil, fmt.Errorf("typesense: initialize store: %w", err)
	}
	return store, nil
}

// initialize creates the collection when
// requested.
func (s *Store) initialize(ctx context.Context, initSchema bool) error {
	if !initSchema {
		return nil
	}
	if s.dimensions <= 0 {
		return errors.New("typesense: Dimensions must be > 0")
	}

	// An existing collection carries its own vec_dist, which decides what
	// vector_distance means, so it is checked rather than assumed.
	if existing, err := s.client.Collection(s.collectionName).Retrieve(ctx); err == nil {
		return s.checkVectorDistance(existing)
	}

	schema := &api.CollectionSchema{
		Name: s.collectionName,
		Fields: []api.Field{
			{Name: idField, Type: "string", Optional: new(false)},
			{Name: contentField, Type: "string", Optional: new(false)},
			{Name: metadataField, Type: "object", Optional: new(true)},
			{
				Name:     embeddingField,
				Type:     "float[]",
				NumDim:   new(s.dimensions),
				Optional: new(false),
				// Cosine is Typesense's default, but the score conversion
				// depends on it, so it is stated rather than inherited.
				VecDist: new(vectorDistanceCosine),
			},
		},
		EnableNestedFields: new(true),
	}
	if _, err := s.client.Collections().Create(ctx, schema); err != nil {
		return fmt.Errorf("typesense: create collection %s: %w", s.collectionName, err)
	}
	return nil
}

// vectorDistanceCosine is the only vec_dist this store can score. Typesense
// also offers "ip", whose vector_distance is not a cosine distance at all.
const vectorDistanceCosine = "cosine"

// checkVectorDistance refuses a collection whose vector field is scored on a
// metric this store cannot read.
//
// vector_distance carries no units: what it means is fixed by the field's
// vec_dist, which defaults to cosine but may be "ip". Reading an inner-product
// distance through the cosine mapping produces plausible scores in the right
// range that rank results wrongly, and no later call can detect it — so a
// host-provisioned collection is rejected at wiring instead.
func (s *Store) checkVectorDistance(schema *api.CollectionResponse) error {
	if schema == nil {
		return fmt.Errorf("typesense: collection %s returned no schema", s.collectionName)
	}
	for _, field := range schema.Fields {
		if field.Name != embeddingField {
			continue
		}
		if distance := lo.FromPtrOr(field.VecDist, vectorDistanceCosine); distance != vectorDistanceCosine {
			return fmt.Errorf(
				"typesense: collection %s field %s uses vec_dist %q; this store scores %q only",
				s.collectionName, embeddingField, distance, vectorDistanceCosine,
			)
		}
		return nil
	}
	return fmt.Errorf("typesense: collection %s has no %s field", s.collectionName, embeddingField)
}

// Index embeds documents and imports them via the upsert action.
func (s *Store) Index(ctx context.Context, request *vectorstore.IndexRequest) (err error) {
	if validateErr := request.Validate(); validateErr != nil {
		return fmt.Errorf("typesense.Store.Index: %w", validateErr)
	}

	var batches []*vectorstore.IndexRequest
	batches, err = request.Batch(ctx, s.documentBatcher)
	if err != nil {
		return fmt.Errorf("typesense: batch documents: %w", err)
	}

	for _, batch := range batches {
		docs := batch.Documents
		texts, err := batch.Texts()
		if err != nil {
			return fmt.Errorf("vectorstore: project document text: %w", err)
		}
		vectors, err := s.embeddingClient.EmbedTexts(ctx, texts)
		if err != nil {
			return fmt.Errorf("typesense: embed documents: %w", err)
		}

		payload := make([]any, 0, len(docs))
		for i, doc := range docs {
			id := doc.ID
			metadataValues, err := doc.Metadata.Values()
			if err != nil {
				return fmt.Errorf("typesense: decode metadata for %s: %w", id, err)
			}
			payload = append(payload, map[string]any{
				idField:        id,
				contentField:   doc.Text,
				metadataField:  lo.CoalesceMapOrEmpty(metadataValues),
				embeddingField: embedding.Float32Vector(vectors[i]),
			})
		}

		params := &api.ImportDocumentsParams{
			Action: new(api.Upsert),
		}
		results, importErr := s.client.Collection(s.collectionName).Documents().Import(ctx, payload, params)
		if importErr != nil {
			return fmt.Errorf("typesense: import documents: %w", importErr)
		}
		if err := checkImportResults(results, docs); err != nil {
			return fmt.Errorf("typesense: import documents: %w", err)
		}
	}
	return nil
}

// checkImportResults reads the per-document outcomes because Typesense answers
// an import with HTTP 200 even when individual documents were rejected. The
// service emits one result per input document in request order, so a missing,
// extra, or unsuccessful entry means the batch was not fully applied.
func checkImportResults(results []*api.ImportDocumentResponse, documents []*document.Document) error {
	if len(results) != len(documents) {
		return fmt.Errorf("import returned %d results for %d documents", len(results), len(documents))
	}
	for index, doc := range documents {
		result := results[index]
		if result == nil {
			return fmt.Errorf("documents[%d] %q has no import result", index, doc.ID)
		}
		if !result.Success {
			return fmt.Errorf("documents[%d] %q: %s", index, doc.ID, result.Error)
		}
	}
	return nil
}

// Search runs semantic vector search or native hybrid search via the
// documents.Search API.
func (s *Store) Search(ctx context.Context, req *vectorstore.SearchRequest) (response *vectorstore.SearchResponse, err error) {
	var docs []*vectorstore.SearchResult
	if err = req.Validate(); err != nil {
		return nil, fmt.Errorf("typesense.Store.Search: %w", err)
	}
	if err = req.Options.RequireMode(vectorstore.SearchModeSemantic, vectorstore.SearchModeHybrid); err != nil {
		return nil, fmt.Errorf("typesense.Store.Search: %w", err)
	}

	defer func() {
		if err == nil {
			err = response.ValidateFor(req)
		}
	}()

	vector, err := s.embeddingClient.EmbedText(ctx, req.Query)
	if err != nil {
		return nil, fmt.Errorf("typesense: embed query: %w", err)
	}
	queryVec := embedding.Float32Vector(vector)
	params, err := s.searchParameters(req, queryVec)
	if err != nil {
		return nil, err
	}

	result, err := s.client.Collection(s.collectionName).Documents().Search(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("typesense: search %s: %w", s.collectionName, err)
	}
	if result == nil || result.Hits == nil {
		return nil, nil
	}

	docs = make([]*vectorstore.SearchResult, 0, len(*result.Hits))
	for rank, hit := range *result.Hits {
		match, err := toMatch(hit, req.Options.EffectiveMode(), rank)
		if err != nil {
			return nil, err
		}
		if match.Score < req.Options.MinScore {
			continue
		}
		docs = append(docs, match)
	}
	return &vectorstore.SearchResponse{Results: docs}, nil
}

func (s *Store) searchParameters(req *vectorstore.SearchRequest, queryVector []float32) (*api.SearchCollectionParams, error) {
	var alpha *float32
	if req.Options.EffectiveMode() == vectorstore.SearchModeHybrid {
		alpha = s.hybridAlpha
	}
	vectorQuery := formatVectorQuery(queryVector, req.Options.ResultLimit(), alpha)
	filterBy, err := s.buildFilter(req.Options.Filter)
	if err != nil {
		return nil, err
	}

	params := &api.SearchCollectionParams{
		Q:           new("*"),
		VectorQuery: new(vectorQuery),
		PerPage:     new(req.Options.ResultLimit()),
	}
	if req.Options.EffectiveMode() == vectorstore.SearchModeHybrid {
		params.Q = new(req.Query)
		params.QueryBy = new(contentField)
	}
	if filterBy != "" {
		params.FilterBy = new(filterBy)
	}
	return params, nil
}

func (s *Store) DeleteWhere(ctx context.Context, expr filter.Predicate) (err error) {
	if expr == nil {
		return vectorstore.ErrMissingFilter
	}
	if err = expr.Validate(); err != nil {
		return fmt.Errorf("typesense.Store.DeleteWhere: %w", err)
	}

	filterBy, err := s.buildFilter(expr)
	if err != nil {
		return err
	}
	if filterBy == "" {
		return errors.New("typesense: refusing to delete on empty filter")
	}

	params := &api.DeleteDocumentsParams{FilterBy: new(filterBy)}
	if _, err := s.client.Collection(s.collectionName).Documents().Delete(ctx, params); err != nil {
		return fmt.Errorf("typesense: delete: %w", err)
	}
	return nil
}

func (s *Store) buildFilter(expr filter.Predicate) (string, error) {
	if expr == nil {
		return "", nil
	}
	v := newVisitor(metadataField)
	if err := expr.Accept(v); err != nil {
		return "", fmt.Errorf("typesense: convert filter: %w", err)
	}
	return v.snapshot(), nil
}

func toMatch(hit api.SearchResultHit, mode vectorstore.SearchMode, rank int) (*vectorstore.SearchResult, error) {
	if hit.Document == nil {
		return nil, errors.New("typesense: search hit is missing document")
	}
	if mode == vectorstore.SearchModeSemantic && hit.VectorDistance == nil {
		return nil, errors.New("typesense: search hit is missing vector distance")
	}
	raw := *hit.Document
	id, ok := raw[idField].(string)
	if !ok || id == "" {
		return nil, fmt.Errorf("typesense: search hit is missing string field %q", idField)
	}
	content, ok := raw[contentField].(string)
	if !ok || content == "" {
		return nil, fmt.Errorf("typesense: search hit is missing string field %q", contentField)
	}
	doc := &document.Document{ID: id, Text: content}
	if meta, ok := raw[metadataField].(map[string]any); ok && len(meta) > 0 {
		var err error
		doc.Metadata, err = metadata.FromValues(meta)
		if err != nil {
			return nil, fmt.Errorf("typesense: convert metadata: %w", err)
		}
	}
	matchScore := scoreFromRank(rank)
	if mode == vectorstore.SearchModeSemantic {
		// Typesense returns distance in the cosine [0, 2] range; map
		// onto a "higher = more similar" score in [0, 1].
		matchScore = vectorstore.ScoreFromCosineDistance(float64(*hit.VectorDistance))
	}
	return &vectorstore.SearchResult{Document: doc, Score: matchScore}, nil
}

func scoreFromRank(rank int) vectorstore.Score {
	return vectorstore.Score(1 / float64(rank+1))
}

// formatVectorQuery builds the Typesense `vector_query` string —
// "embedding:([f1,f2,...], k: N)".
func formatVectorQuery(vec []float32, topK int, alpha *float32) string {
	var b strings.Builder
	b.WriteString(embeddingField)
	b.WriteString(":([")
	for i, f := range vec {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(float64(f), 'f', -1, 32))
	}
	b.WriteString("], k: ")
	b.WriteString(strconv.Itoa(topK))
	if alpha != nil {
		b.WriteString(", alpha: ")
		b.WriteString(strconv.FormatFloat(float64(*alpha), 'f', -1, 32))
	}
	b.WriteByte(')')
	return b.String()
}
