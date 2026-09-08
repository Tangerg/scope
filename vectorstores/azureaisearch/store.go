package azureaisearch

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/embeddingclient"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

// SimilarityMetric records the metric configured on the existing Azure AI
// Search vector field.
type SimilarityMetric string

// ErrIncompatibleIndex reports an index that cannot serve this store: the
// configured vector field is missing or unsearchable, or the algorithm behind
// it was configured with a different similarity metric.
var ErrIncompatibleIndex = errors.New("azureaisearch: index is incompatible")

// The metric is a closed vocabulary because score direction and threshold
// semantics depend on it: the same raw number means "near" under one metric and
// "far" under another, so an unrecognized value must be rejected rather than
// guessed.
const (
	SimilarityCosine    SimilarityMetric = "cosine"
	SimilarityDot       SimilarityMetric = "dotProduct"
	SimilarityEuclidean SimilarityMetric = "euclidean"
)

func (s SimilarityMetric) Valid() bool {
	switch s {
	case SimilarityCosine, SimilarityDot, SimilarityEuclidean:
		return true
	default:
		return false
	}
}

func (s SimilarityMetric) String() string { return string(s) }

// score maps @search.score, which is never the raw metric value: Azure applies
// a transformation so the score falls monotonically as the match worsens.
func (s SimilarityMetric) score(raw float64) vectorstore.Score {
	switch s {
	case SimilarityCosine:
		// Documented exactly: "@search.score is defined as
		// 1 / (1 + cosine_distance)", giving 0.333 to 1.00. Invert it to
		// recover the cosine, then apply the [-1, 1] to [0, 1] mapping.
		cosineDistance := 1/raw - 1
		return vectorstore.ScoreFromCosineSimilarity(1 - cosineDistance)
	default:
		// Azure states the transformation and the range for cosine only, so
		// there is no published formula to invert for dotProduct or euclidean.
		// Clamping keeps the ranking Azure already applied and refuses to
		// invent a conversion; a value outside [0, 1] would flatten onto the
		// bound rather than be silently rescaled by a guess.
		return vectorstore.ScoreFromValue(raw)
	}
}

// Exported identifiers keep provider-owned names and defaults out of caller literals.
const (
	Provider = "AzureAISearch"

	// Azure AI Search rejects document batches above this service limit.
	maximumDocumentsPerBatch = 1000

	// DefaultAPIVersion targets the GA "2024-07-01" REST surface, the
	// first stable release that exposes the typed vector-query
	// payload used by the Scope store.
	DefaultAPIVersion = "2024-07-01"

	// DefaultContentField / DefaultEmbeddingField / DefaultIDField
	// name the well-known fields written to and read from each
	// document. They must exist on the underlying index schema.
	DefaultContentField     = "content"
	DefaultEmbeddingField   = "contentVector"
	DefaultIDField          = "id"
	DefaultMaxResponseBytes = int64(16 * 1024 * 1024)
)

// StoreConfig contains configuration options for the Azure AI Search
// vector store. The store talks to the REST surface directly — Azure
// doesn't ship a typed Go SDK for the Search service.
type StoreConfig struct {
	// Endpoint is the search service URL, e.g.
	// "https://my-search.search.windows.net". Required.
	Endpoint string

	// APIKey is the admin API key. Required for both read and write.
	// Use Managed Identity / OAuth via [HTTPClient] for finer
	// authorization control.
	APIKey string

	// IndexName is the index to operate on. Required. The schema
	// must already contain the configured ID, content, vector, and
	// metadata fields — Azure AI Search index schemas are typed and
	// cannot be created lazily.
	IndexName string

	// APIVersion overrides the REST API version. Optional: defaults
	// to [DefaultAPIVersion].
	APIVersion string

	// IDField / ContentField / EmbeddingField name the well-known
	// fields on each document. Optional defaults apply. These fields must
	// differ and cannot use protocol annotation names beginning with @.
	IDField        string
	ContentField   string
	EmbeddingField string

	// EmbeddingModel produces vectors for the documents. Required.
	EmbeddingModel embedding.Model

	// DocumentBatcher batches documents before upsert. Required.
	DocumentBatcher vectorstore.Batcher

	// SimilarityMetric must match the metric in the index's vector-search
	// algorithm configuration. Required because @search.score is metric-specific.
	SimilarityMetric SimilarityMetric

	// HTTPClient lets callers override transport (timeouts,
	// proxies, MSAL bearer-token injection). Optional: defaults to
	// http.DefaultClient.
	HTTPClient *http.Client

	// MaxResponseBytes bounds every buffered HTTP response. Zero selects
	// [DefaultMaxResponseBytes].
	MaxResponseBytes int64
}

func (s StoreConfig) Validate() error {
	s.applyDefaults()
	if s.Endpoint == "" {
		return errors.New("azureaisearch: Endpoint is required")
	}
	if s.APIKey == "" {
		return errors.New("azureaisearch: APIKey is required")
	}
	if s.IndexName == "" {
		return errors.New("azureaisearch: IndexName is required")
	}
	if lo.IsNil(s.EmbeddingModel) {
		return errors.New("azureaisearch: EmbeddingModel is required")
	}
	if lo.IsNil(s.DocumentBatcher) {
		return errors.New("azureaisearch: DocumentBatcher is required")
	}
	if s.SimilarityMetric == "" {
		return errors.New("azureaisearch: SimilarityMetric is required")
	}
	if !s.SimilarityMetric.Valid() {
		return fmt.Errorf("azureaisearch: unsupported SimilarityMetric %q", s.SimilarityMetric)
	}
	if s.MaxResponseBytes < 0 {
		return errors.New("azureaisearch: MaxResponseBytes must not be negative")
	}
	fields := []string{s.IDField, s.ContentField, s.EmbeddingField}
	seen := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		if strings.HasPrefix(field, "@") {
			return fmt.Errorf("azureaisearch: storage field %q is reserved for protocol annotations", field)
		}
		if _, duplicate := seen[field]; duplicate {
			return errors.New("azureaisearch: IDField, ContentField, and EmbeddingField must be distinct")
		}
		seen[field] = struct{}{}
	}
	return nil
}

// applyDefaults fills zero fields with documented defaults.
func (s *StoreConfig) applyDefaults() {
	s.APIVersion = cmp.Or(s.APIVersion, DefaultAPIVersion)
	s.IDField = cmp.Or(s.IDField, DefaultIDField)
	s.ContentField = cmp.Or(s.ContentField, DefaultContentField)
	s.EmbeddingField = cmp.Or(s.EmbeddingField, DefaultEmbeddingField)
	if s.HTTPClient == nil {
		s.HTTPClient = http.DefaultClient
	}
}

var (
	_ vectorstore.Indexer       = (*Store)(nil)
	_ vectorstore.Searcher      = (*Store)(nil)
	_ vectorstore.FilterDeleter = (*Store)(nil)
)

// Store implements vector-store capabilities through the Azure AI Search REST
// API.
type Store struct {
	endpoint         string
	apiKey           string
	indexName        string
	apiVersion       string
	idField          string
	contentField     string
	embeddingField   string
	embeddingClient  embeddingclient.Client
	documentBatcher  vectorstore.Batcher
	similarityMetric SimilarityMetric
	httpClient       *http.Client
	maxResponseBytes int64
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
		return nil, fmt.Errorf("azureaisearch: create embedding client: %w", err)
	}

	store := &Store{
		endpoint:         strings.TrimRight(config.Endpoint, "/"),
		apiKey:           config.APIKey,
		indexName:        config.IndexName,
		apiVersion:       config.APIVersion,
		idField:          config.IDField,
		contentField:     config.ContentField,
		embeddingField:   config.EmbeddingField,
		embeddingClient:  embeddingClient,
		documentBatcher:  config.DocumentBatcher,
		similarityMetric: config.SimilarityMetric,
		httpClient:       config.HTTPClient,
		maxResponseBytes: cmp.Or(config.MaxResponseBytes, DefaultMaxResponseBytes),
	}
	if err = store.verifyIndexMetric(ctx); err != nil {
		return nil, err
	}
	return store, nil
}

// indexSchema is the part of an index definition this store has to agree with.
// A vector field names a profile, the profile names an algorithm, and only the
// algorithm carries the metric, so the metric is three hops from the field.
type indexSchema struct {
	Fields []struct {
		Name                string `json:"name"`
		VectorSearchProfile string `json:"vectorSearchProfile"`
	} `json:"fields"`
	VectorSearch struct {
		Profiles []struct {
			Name      string `json:"name"`
			Algorithm string `json:"algorithm"`
		} `json:"profiles"`
		Algorithms []struct {
			Name           string `json:"name"`
			HNSWParameters struct {
				Metric SimilarityMetric `json:"metric"`
			} `json:"hnswParameters"`
			ExhaustiveKNNParameters struct {
				Metric SimilarityMetric `json:"metric"`
			} `json:"exhaustiveKnnParameters"`
		} `json:"algorithms"`
	} `json:"vectorSearch"`
}

func (s *Store) verifyIndexMetric(ctx context.Context) error {
	raw, err := s.sendJSON(ctx, http.MethodGet, "/indexes/"+url.PathEscape(s.indexName), nil)
	if err != nil {
		return fmt.Errorf("azureaisearch: read index %s: %w", s.indexName, err)
	}
	var schema indexSchema
	if err = json.Unmarshal(raw, &schema); err != nil {
		return fmt.Errorf("azureaisearch: decode index %s: %w", s.indexName, err)
	}
	return validateIndexMetric(&schema, s.embeddingField, s.similarityMetric)
}

// validateIndexMetric refuses a store whose configured metric is not the one
// the vector field's algorithm was configured with.
//
// @search.score is metric-specific, so a wrong value does not fail: the store
// applies the wrong transformation and returns plausible scores that are wrong,
// with MinScore filtering by a threshold in the wrong scale. Nothing
// downstream can notice.
//
// Dimensions are deliberately not compared. This store declares none, and
// Azure rejects a vector of the wrong width on upload.
func validateIndexMetric(schema *indexSchema, embeddingField string, want SimilarityMetric) error {
	profileName := ""
	found := false
	for _, field := range schema.Fields {
		if field.Name == embeddingField {
			profileName = field.VectorSearchProfile
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("%w: the index declares no field named %q", ErrIncompatibleIndex, embeddingField)
	}
	if profileName == "" {
		return fmt.Errorf("%w: field %q names no vectorSearchProfile, so it is not searchable as a vector",
			ErrIncompatibleIndex, embeddingField)
	}

	algorithmName := ""
	for _, profile := range schema.VectorSearch.Profiles {
		if profile.Name == profileName {
			algorithmName = profile.Algorithm
			break
		}
	}
	if algorithmName == "" {
		return fmt.Errorf("%w: vectorSearchProfile %q on field %q resolves to no algorithm",
			ErrIncompatibleIndex, profileName, embeddingField)
	}

	for _, algorithm := range schema.VectorSearch.Algorithms {
		if algorithm.Name != algorithmName {
			continue
		}
		// Exactly one parameter block is populated, chosen by the algorithm's
		// kind; reading whichever carries a metric avoids depending on a kind
		// string this store has no other use for.
		metric := cmp.Or(algorithm.HNSWParameters.Metric, algorithm.ExhaustiveKNNParameters.Metric)
		if metric == "" {
			return fmt.Errorf("%w: algorithm %q declares no metric", ErrIncompatibleIndex, algorithmName)
		}
		if metric != want {
			return fmt.Errorf("%w: field %q searches through algorithm %q with metric %q, but the store is configured for %q",
				ErrIncompatibleIndex, embeddingField, algorithmName, metric, want)
		}
		return nil
	}
	return fmt.Errorf("%w: the index declares no algorithm named %q", ErrIncompatibleIndex, algorithmName)
}

// Index validates metadata ownership across the full request, embeds documents,
// and uploads them through acknowledged batches of at most 1000 actions.
func (s *Store) Index(ctx context.Context, request *vectorstore.IndexRequest) (err error) {
	if validateErr := request.Validate(); validateErr != nil {
		return fmt.Errorf("azureaisearch.Store.Index: %w", validateErr)
	}
	for index, item := range request.Documents {
		for field := range item.Metadata {
			if s.reservedField(field) {
				return fmt.Errorf("%w: azureaisearch: documents[%d] metadata field %q is reserved", vectorstore.ErrInvalidDocument, index, field)
			}
		}
	}

	var batches []*vectorstore.IndexRequest
	batches, err = request.Batch(ctx, s.documentBatcher)
	if err != nil {
		return fmt.Errorf("azureaisearch: batch documents: %w", err)
	}

	for _, batch := range batches {
		docs := batch.Documents
		texts, err := batch.Texts()
		if err != nil {
			return fmt.Errorf("vectorstore: project document text: %w", err)
		}
		vectors, err := s.embeddingClient.EmbedTexts(ctx, texts)
		if err != nil {
			return fmt.Errorf("azureaisearch: embed documents: %w", err)
		}

		actions := make([]map[string]any, 0, len(docs))
		for i, doc := range docs {
			id := doc.ID
			metadataValues, err := doc.Metadata.Values()
			if err != nil {
				return fmt.Errorf("azureaisearch: decode metadata for %s: %w", id, err)
			}
			payload := map[string]any{
				"@search.action": "mergeOrUpload",
				s.idField:        id,
				s.contentField:   doc.Text,
				s.embeddingField: embedding.Float32Vector(vectors[i]),
			}
			// Top-level metadata fields — caller is responsible for
			// having declared them in the index schema.
			maps.Copy(payload, metadataValues)
			actions = append(actions, payload)
		}

		if err := s.writeActions(ctx, actions); err != nil {
			return fmt.Errorf("azureaisearch: index documents: %w", err)
		}
	}
	return nil
}

// Search runs a semantic vector query or a native hybrid query that combines
// the same vector with lexical evidence from the configured content field.
func (s *Store) Search(ctx context.Context, req *vectorstore.SearchRequest) (response *vectorstore.SearchResponse, err error) {
	var docs []*vectorstore.SearchResult
	if err = req.Validate(); err != nil {
		return nil, fmt.Errorf("azureaisearch.Store.Search: %w", err)
	}
	if err = req.Options.RequireMode(vectorstore.SearchModeSemantic, vectorstore.SearchModeHybrid); err != nil {
		return nil, fmt.Errorf("azureaisearch.Store.Search: %w", err)
	}

	defer func() {
		if err == nil {
			err = response.ValidateFor(req)
		}
	}()

	vector, err := s.embeddingClient.EmbedText(ctx, req.Query)
	if err != nil {
		return nil, fmt.Errorf("azureaisearch: embed query: %w", err)
	}
	queryVec := embedding.Float32Vector(vector)

	filterStr, err := s.buildFilter(req.Options.Filter)
	if err != nil {
		return nil, err
	}

	vectorQuery := map[string]any{
		"kind":   "vector",
		"vector": queryVec,
		"k":      req.Options.ResultLimit(),
		"fields": s.embeddingField,
	}
	body := map[string]any{
		"count":         false,
		"top":           req.Options.ResultLimit(),
		"vectorQueries": []any{vectorQuery},
	}
	if req.Options.EffectiveMode() == vectorstore.SearchModeHybrid {
		body["search"] = req.Query
		body["searchFields"] = s.contentField
	}
	if filterStr != "" {
		body["filter"] = filterStr
	}

	rows, err := s.searchDocuments(ctx, body)
	if err != nil {
		return nil, fmt.Errorf("azureaisearch: search: %w", err)
	}

	docs = make([]*vectorstore.SearchResult, 0, len(rows))
	for _, row := range rows {
		match, err := s.toMatch(row, req.Options.EffectiveMode())
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

func (s *Store) DeleteWhere(ctx context.Context, expr filter.Predicate) (err error) {
	if expr == nil {
		return vectorstore.ErrMissingFilter
	}
	if err = expr.Validate(); err != nil {
		return fmt.Errorf("azureaisearch.Store.DeleteWhere: %w", err)
	}

	filterStr, err := s.buildFilter(expr)
	if err != nil {
		return err
	}
	if filterStr == "" {
		return errors.New("azureaisearch: refusing to delete on empty filter")
	}

	// Page through ids matching the filter.
	const pageSize = 1000
	ids := make([]string, 0, pageSize)
	skip := 0
	for {
		body := map[string]any{
			"select": s.idField,
			"filter": filterStr,
			"top":    pageSize,
			"skip":   skip,
		}
		rows, err := s.searchDocuments(ctx, body)
		if err != nil {
			return fmt.Errorf("azureaisearch: enumerate ids: %w", err)
		}
		for _, row := range rows {
			id, err := s.documentID(row)
			if err != nil {
				return err
			}
			ids = append(ids, id)
		}
		if len(rows) < pageSize {
			break
		}
		skip += len(rows)
	}

	if len(ids) == 0 {
		return nil
	}

	actions := make([]map[string]any, len(ids))
	for index, id := range ids {
		actions[index] = map[string]any{"@search.action": "delete", s.idField: id}
	}
	if err := s.writeActions(ctx, actions); err != nil {
		return fmt.Errorf("azureaisearch: delete documents: %w", err)
	}
	return nil
}

func (s *Store) searchDocuments(ctx context.Context, body any) ([]metadata.Map, error) {
	path := fmt.Sprintf("/indexes/%s/docs/search", url.PathEscape(s.indexName))
	var rows []metadata.Map
	for {
		raw, err := s.sendJSON(ctx, http.MethodPost, path, body)
		if err != nil {
			return nil, err
		}
		var page struct {
			Value          []metadata.Map             `json:"value"`
			NextParameters map[string]json.RawMessage `json:"@search.nextPageParameters"`
			NextLink       string                     `json:"@odata.nextLink"`
		}
		if err := json.Unmarshal(raw, &page); err != nil {
			return nil, fmt.Errorf("decode search response: %w", err)
		}
		if page.Value == nil {
			return nil, errors.New("search response is missing its result array")
		}
		rows = append(rows, page.Value...)
		if len(page.NextParameters) == 0 {
			if page.NextLink != "" {
				return nil, errors.New("search continuation is missing POST parameters")
			}
			return rows, nil
		}
		// POST continuations carry the complete next request. The configured
		// index endpoint retains authority over where credentials are sent.
		body = page.NextParameters
	}
}

func (s *Store) writeActions(ctx context.Context, actions []map[string]any) error {
	path := fmt.Sprintf("/indexes/%s/docs/index", url.PathEscape(s.indexName))
	for batch := range slices.Chunk(actions, maximumDocumentsPerBatch) {
		raw, err := s.sendJSON(ctx, http.MethodPost, path, map[string]any{"value": batch})
		if err != nil {
			return err
		}
		var response struct {
			Value []struct {
				Key          string `json:"key"`
				Status       bool   `json:"status"`
				StatusCode   int    `json:"statusCode"`
				ErrorMessage string `json:"errorMessage"`
			} `json:"value"`
		}
		if err := json.Unmarshal(raw, &response); err != nil {
			return fmt.Errorf("decode write response: %w", err)
		}
		if len(response.Value) != len(batch) {
			return fmt.Errorf("write response contains %d results for %d actions", len(response.Value), len(batch))
		}
		pending := make(map[string]struct{}, len(batch))
		for _, action := range batch {
			pending[action[s.idField].(string)] = struct{}{}
		}
		for _, result := range response.Value {
			if _, expected := pending[result.Key]; !expected {
				return fmt.Errorf("write response contains unknown or repeated key %q", result.Key)
			}
			delete(pending, result.Key)
			if !result.Status {
				return fmt.Errorf("document %q: status=%d: %s", result.Key, result.StatusCode, result.ErrorMessage)
			}
		}
	}
	return nil
}

func (s *Store) reservedField(field string) bool {
	return field == s.idField || field == s.contentField || field == s.embeddingField || strings.HasPrefix(field, "@")
}

func (s *Store) buildFilter(expr filter.Predicate) (string, error) {
	if expr == nil {
		return "", nil
	}
	v := newVisitor()
	if err := expr.Accept(v); err != nil {
		return "", fmt.Errorf("azureaisearch: convert filter: %w", err)
	}
	return v.snapshot(), nil
}

func (s *Store) documentID(row metadata.Map) (string, error) {
	id, present, err := row.Decode[string](s.idField)
	if err != nil {
		return "", fmt.Errorf("azureaisearch: decode document ID: %w", err)
	}
	if !present || id == "" {
		return "", fmt.Errorf("azureaisearch: result is missing string field %q", s.idField)
	}
	return id, nil
}

func (s *Store) toMatch(row metadata.Map, mode vectorstore.SearchMode) (*vectorstore.SearchResult, error) {
	id, err := s.documentID(row)
	if err != nil {
		return nil, err
	}
	text, present, err := row.Decode[string](s.contentField)
	if err != nil {
		return nil, fmt.Errorf("azureaisearch: decode content: %w", err)
	}
	if !present || text == "" {
		return nil, fmt.Errorf("azureaisearch: result is missing string field %q", s.contentField)
	}
	rawScore, present, err := row.Decode[*float64]("@search.score")
	if err != nil {
		return nil, fmt.Errorf("azureaisearch: decode score: %w", err)
	}
	if !present || rawScore == nil {
		return nil, errors.New("azureaisearch: result is missing numeric @search.score")
	}
	score := vectorstore.ScoreFromValue(*rawScore)
	if mode == vectorstore.SearchModeSemantic {
		score = s.similarityMetric.score(*rawScore)
	}

	// Rows are freshly decoded; transfer their raw metadata without passing
	// integer values through a lossy floating-point representation.
	for field := range row {
		if s.reservedField(field) {
			delete(row, field)
		}
	}
	return &vectorstore.SearchResult{
		Document: &document.Document{ID: id, Text: text, Metadata: row}, Score: score,
	}, nil
}

func (s *Store) sendJSON(ctx context.Context, method, path string, body any) ([]byte, error) {
	u := fmt.Sprintf("%s%s?api-version=%s", s.endpoint, path, url.QueryEscape(s.apiVersion))

	var reqBody io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("encode request: %w", err)
		}
		reqBody = bytes.NewReader(buf)
	}

	req, err := http.NewRequestWithContext(ctx, method, u, reqBody)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("api-key", s.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()

	maxResponseBytes := cmp.Or(s.maxResponseBytes, DefaultMaxResponseBytes)
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if int64(len(respBody)) > maxResponseBytes {
		return nil, fmt.Errorf("response exceeds %d-byte limit", maxResponseBytes)
	}
	if resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("status=%d body=%s", resp.StatusCode, string(respBody))
	}
	return respBody, nil
}
