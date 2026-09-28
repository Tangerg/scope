package typesense

import (
	"cmp"
	"context"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
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
	idField               = "id"
	contentField          = "content"
	metadataField         = "metadata"
	embeddingField        = "embedding"
	// MaxResultsPerPage is Typesense's documented search pagination limit.
	MaxResultsPerPage = 250
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

// storedDocument retains metadata numbers from raw provider JSON. Decoding
// through the SDK's map[string]any projection would first round them to float64.
type storedDocument struct {
	ID       string       `json:"id"`
	Content  string       `json:"content"`
	Metadata metadata.Map `json:"metadata"`
}

type searchHit struct {
	Document       *storedDocument `json:"document"`
	VectorDistance *float32        `json:"vector_distance"`
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
	existing, err := s.client.Collection(s.collectionName).Retrieve(ctx)
	if err == nil {
		return s.checkVectorDistance(existing)
	}
	var httpErr *typesense.HTTPError
	if !initSchema || !errors.As(err, &httpErr) || httpErr.Status != http.StatusNotFound {
		return fmt.Errorf("typesense: retrieve collection %s: %w", s.collectionName, err)
	}
	if s.dimensions <= 0 {
		return errors.New("typesense: Dimensions must be > 0")
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
	for index, doc := range request.Documents {
		if doc.Media != nil {
			return fmt.Errorf("typesense.Store.Index: %w: documents[%d] contains unsupported media", vectorstore.ErrInvalidDocument, index)
		}
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

// Search runs semantic vector search or native hybrid search. A filter first
// exports all document metadata and evaluates membership with [filter.Match],
// then restricts native ranking to the resulting IDs. This requires permission
// to export documents and reads the full collection. Search pages contain at
// most [MaxResultsPerPage] hits. Concurrent writes are not isolated by the
// export and subsequent search.
func (s *Store) Search(ctx context.Context, req *vectorstore.SearchRequest) (response *vectorstore.SearchResponse, err error) {
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

	var filterBy string
	var selectedIDs map[string]struct{}
	if req.Options.Filter != nil {
		ids, matchErr := s.matchingIDs(ctx, req.Options.Filter)
		if matchErr != nil {
			return nil, matchErr
		}
		if len(ids) == 0 {
			return &vectorstore.SearchResponse{}, nil
		}
		filterBy, err = idFilter(ids)
		if err != nil {
			return nil, err
		}
		selectedIDs = make(map[string]struct{}, len(ids))
		for _, id := range ids {
			selectedIDs[id] = struct{}{}
		}
	}

	vector, err := s.embeddingClient.EmbedText(ctx, req.Query)
	if err != nil {
		return nil, fmt.Errorf("typesense: embed query: %w", err)
	}
	queryVec := embedding.Float32Vector(vector)
	params := s.searchParameters(req, queryVec)
	if filterBy != "" {
		params.FilterBy = new(filterBy)
	}
	limit := req.Options.ResultLimit()
	var docs []*vectorstore.SearchResult
	var ranked int
	for page := 1; ; page++ {
		params.Page = new(page)
		// Carry the complete ID set and vector in a POST body, avoiding URL
		// length limits when a filter matches a large collection.
		wire, err := s.client.MultiSearch.PerformWithContentType(ctx, nil, api.MultiSearchSearchesParameter{
			Searches: []api.MultiSearchCollectionParameters{*params},
		}, "application/json")
		if err != nil {
			return nil, fmt.Errorf("typesense: search %s: %w", s.collectionName, err)
		}
		if wire == nil {
			return nil, errors.New("typesense: multi-search returned no response")
		}
		if wire.StatusCode() != http.StatusOK {
			return nil, fmt.Errorf("typesense: search %s: %w", s.collectionName, &typesense.HTTPError{Status: wire.StatusCode(), Body: wire.Body})
		}
		var batch struct {
			Results []struct {
				Code         *int         `json:"code"`
				Error        *string      `json:"error"`
				Found        *int         `json:"found"`
				Hits         *[]searchHit `json:"hits"`
				SearchCutoff *bool        `json:"search_cutoff"`
			} `json:"results"`
		}
		if err := jsonv2.Unmarshal(wire.Body, &batch); err != nil {
			return nil, fmt.Errorf("typesense: decode search response: %w", err)
		}
		if len(batch.Results) != 1 {
			return nil, errors.New("typesense: multi-search did not return exactly one result")
		}
		result := &batch.Results[0]
		if result.Error != nil || (result.Code != nil && *result.Code >= 400) {
			return nil, fmt.Errorf("typesense: search %s returned code %d: %s", s.collectionName, lo.FromPtr(result.Code), lo.FromPtr(result.Error))
		}
		if result.Hits == nil {
			return nil, errors.New("typesense: search response is missing hits")
		}
		if result.SearchCutoff != nil && *result.SearchCutoff {
			return nil, errors.New("typesense: search was cut off before completion")
		}
		for _, hit := range *result.Hits {
			if ranked == limit {
				break
			}
			match, err := toMatch(hit, req.Options.EffectiveMode(), ranked)
			if err != nil {
				return nil, err
			}
			if req.Options.Filter != nil {
				if _, exists := selectedIDs[match.Document.ID]; !exists {
					return nil, fmt.Errorf("typesense: search returned unselected ID %q", match.Document.ID)
				}
				values, err := match.Document.Metadata.Values()
				if err != nil {
					return nil, fmt.Errorf("typesense: decode returned metadata: %w", err)
				}
				matches, err := filter.Match(req.Options.Filter, values)
				if err != nil {
					return nil, fmt.Errorf("typesense: evaluate returned metadata for %s: %w", match.Document.ID, err)
				}
				if !matches {
					return nil, fmt.Errorf("typesense: returned metadata for %s no longer matches the filter", match.Document.ID)
				}
			}
			ranked++
			if match.Score >= req.Options.MinScore {
				docs = append(docs, match)
			}
		}
		if ranked == limit || (result.Found != nil && ranked >= *result.Found) {
			return &vectorstore.SearchResponse{Results: docs}, nil
		}
		if len(*result.Hits) < *params.PerPage {
			if result.Found != nil && ranked < *result.Found {
				return nil, errors.New("typesense: search page ended before the reported result count")
			}
			return &vectorstore.SearchResponse{Results: docs}, nil
		}
	}
}

func (s *Store) searchParameters(req *vectorstore.SearchRequest, queryVector []float32) *api.MultiSearchCollectionParameters {
	var alpha *float32
	if req.Options.EffectiveMode() == vectorstore.SearchModeHybrid {
		alpha = s.hybridAlpha
	}
	vectorQuery := formatVectorQuery(queryVector, req.Options.ResultLimit(), alpha)
	params := &api.MultiSearchCollectionParameters{
		Collection:  new(s.collectionName),
		Q:           new("*"),
		VectorQuery: new(vectorQuery),
		PerPage:     new(min(req.Options.ResultLimit(), MaxResultsPerPage)),
		// Curated hits must obey the same filter as ordinary ranked hits.
		FilterCuratedHits: new(true),
	}
	if req.Options.EffectiveMode() == vectorstore.SearchModeHybrid {
		params.Q = new(req.Query)
		params.QueryBy = new(contentField)
	}
	return params
}

// DeleteWhere exports and evaluates the entire collection before deleting the
// matching IDs. Export, decoding, predicate and ID representation errors abort
// before any deletion. It requires document export permission in addition to
// delete permission and does not isolate concurrent writes.
func (s *Store) DeleteWhere(ctx context.Context, expr filter.Predicate) (err error) {
	if expr == nil {
		return vectorstore.ErrMissingFilter
	}
	if err = expr.Validate(); err != nil {
		return fmt.Errorf("typesense.Store.DeleteWhere: %w", err)
	}

	ids, err := s.matchingIDs(ctx, expr)
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}
	filterBy, err := idFilter(ids)
	if err != nil {
		return err
	}

	params := &api.DeleteDocumentsParams{FilterBy: new(filterBy)}
	if _, err := s.client.Collection(s.collectionName).Documents().Delete(ctx, params); err != nil {
		return fmt.Errorf("typesense: delete: %w", err)
	}
	return nil
}

func (s *Store) matchingIDs(ctx context.Context, expr filter.Predicate) (ids []string, err error) {
	body, err := s.client.Collection(s.collectionName).Documents().Export(ctx, &api.ExportDocumentsParams{
		IncludeFields: new(idField + "," + metadataField),
	})
	if err != nil {
		return nil, fmt.Errorf("typesense: export metadata: %w", err)
	}
	defer func() {
		if closeErr := body.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("typesense: close metadata export: %w", closeErr))
		}
	}()
	decoder := jsontext.NewDecoder(body)
	for {
		var doc storedDocument
		if err := jsonv2.UnmarshalDecode(decoder, &doc); err != nil {
			if errors.Is(err, io.EOF) {
				return ids, nil
			}
			return nil, fmt.Errorf("typesense: decode metadata export: %w", err)
		}
		if doc.ID == "" {
			return nil, errors.New("typesense: exported document is missing its ID")
		}
		values, err := doc.Metadata.Values()
		if err != nil {
			return nil, fmt.Errorf("typesense: decode metadata for %s: %w", doc.ID, err)
		}
		matched, err := filter.Match(expr, values)
		if err != nil {
			return nil, fmt.Errorf("typesense: evaluate filter for %s: %w", doc.ID, err)
		}
		if matched {
			ids = append(ids, doc.ID)
		}
	}
}

// idFilter addresses Typesense's special ID field, which looks up exact keys.
// Its parser trims ASCII edge spaces, treats a sole * as a wildcard even in
// quotes, and cannot reliably preserve embedded backticks or trailing
// backslashes in quoted values. Refuse those IDs
// only when they match a filtered operation, before any search or deletion.
func idFilter(ids []string) (string, error) {
	var result strings.Builder
	result.WriteString(idField + ":=[")
	for index, id := range ids {
		if id == "" || id == "*" || strings.Trim(id, " ") != id || strings.ContainsRune(id, '`') || strings.HasSuffix(id, `\`) {
			return "", fmt.Errorf("typesense: matched ID %q cannot be represented exactly in an ID filter", id)
		}
		if index != 0 {
			result.WriteByte(',')
		}
		result.WriteByte('`')
		result.WriteString(id)
		result.WriteByte('`')
	}
	result.WriteByte(']')
	return result.String(), nil
}

func toMatch(hit searchHit, mode vectorstore.SearchMode, rank int) (*vectorstore.SearchResult, error) {
	if hit.Document == nil {
		return nil, errors.New("typesense: search hit is missing document")
	}
	if mode == vectorstore.SearchModeSemantic && hit.VectorDistance == nil {
		return nil, errors.New("typesense: search hit is missing vector distance")
	}
	raw := *hit.Document
	id := raw.ID
	if id == "" {
		return nil, fmt.Errorf("typesense: search hit is missing string field %q", idField)
	}
	content := raw.Content
	if content == "" {
		return nil, fmt.Errorf("typesense: search hit is missing string field %q", contentField)
	}
	doc := &document.Document{ID: id, Text: content, Metadata: raw.Metadata}
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
