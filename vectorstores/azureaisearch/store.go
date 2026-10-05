package azureaisearch

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"math"
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

const (
	Provider                 = "AzureAISearch"
	DefaultAPIVersion        = "2024-07-01"
	DefaultIDField           = "id"
	DefaultContentField      = "content"
	DefaultEmbeddingField    = "contentVector"
	DefaultMetadataField     = "scope_metadata"
	DefaultMaxResponseBytes  = int64(16 * 1024 * 1024)
	maximumDocumentsPerBatch = 1000
	maximumResultsPerPage    = 1000
	maximumRequestBytes      = 16 * 1024 * 1024
)

// StoreConfig binds an existing index. The host owns schema creation,
// authentication, transport timeouts, retries and the HTTP client's lifecycle.
type StoreConfig struct {
	Endpoint         string
	IndexName        string
	APIVersion       string
	IDField          string
	ContentField     string
	EmbeddingField   string
	MetadataField    string
	EmbeddingModel   embedding.Model
	DocumentBatcher  vectorstore.Batcher
	HTTPClient       *http.Client
	MaxResponseBytes int64
}

func (s *StoreConfig) applyDefaults() {
	s.APIVersion = cmp.Or(s.APIVersion, DefaultAPIVersion)
	s.IDField = cmp.Or(s.IDField, DefaultIDField)
	s.ContentField = cmp.Or(s.ContentField, DefaultContentField)
	s.EmbeddingField = cmp.Or(s.EmbeddingField, DefaultEmbeddingField)
	s.MetadataField = cmp.Or(s.MetadataField, DefaultMetadataField)
	s.MaxResponseBytes = cmp.Or(s.MaxResponseBytes, DefaultMaxResponseBytes)
}

func (s StoreConfig) Validate() error {
	s.applyDefaults()
	endpoint, err := url.Parse(s.Endpoint)
	if err != nil || endpoint.Host == "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || strings.Trim(endpoint.Path, "/") != "" {
		return errors.New("azureaisearch: Endpoint must be an HTTP service origin")
	}
	if s.IndexName == "" {
		return errors.New("azureaisearch: IndexName is required")
	}
	if s.HTTPClient == nil {
		return errors.New("azureaisearch: HTTPClient is required")
	}
	if lo.IsNil(s.EmbeddingModel) {
		return errors.New("azureaisearch: EmbeddingModel is required")
	}
	if lo.IsNil(s.DocumentBatcher) {
		return errors.New("azureaisearch: DocumentBatcher is required")
	}
	if s.MaxResponseBytes < 0 || s.MaxResponseBytes == math.MaxInt64 {
		return errors.New("azureaisearch: MaxResponseBytes must be nonnegative and below MaxInt64")
	}
	seen := make(map[string]struct{}, 4)
	for _, field := range []string{s.IDField, s.ContentField, s.EmbeddingField, s.MetadataField} {
		if !validFieldName(field) {
			return fmt.Errorf("azureaisearch: invalid native field name %q", field)
		}
		if _, duplicate := seen[field]; duplicate {
			return errors.New("azureaisearch: storage fields must be distinct")
		}
		seen[field] = struct{}{}
	}
	return nil
}

var (
	_ vectorstore.Indexer   = (*Store)(nil)
	_ vectorstore.Searcher  = (*Store)(nil)
	_ vectorstore.IDDeleter = (*Store)(nil)
)

// Store projects native schema facts and uses Core's metadata codec and
// predicate evaluator. It never owns a second metadata filter language.
type Store struct {
	endpoint         string
	indexName        string
	apiVersion       string
	idField          string
	contentField     string
	embeddingField   string
	metadataField    string
	embeddingClient  embeddingclient.Client
	documentBatcher  vectorstore.Batcher
	httpClient       *http.Client
	maxResponseBytes int64
	metric           nativeMetric
	dimensions       int
}

func NewStore(ctx context.Context, config StoreConfig) (*Store, error) {
	config.applyDefaults()
	if err := config.Validate(); err != nil {
		return nil, err
	}
	client, err := embeddingclient.New(config.EmbeddingModel)
	if err != nil {
		return nil, err
	}
	store := &Store{
		endpoint: strings.TrimRight(config.Endpoint, "/"), indexName: config.IndexName, apiVersion: config.APIVersion,
		idField: config.IDField, contentField: config.ContentField, embeddingField: config.EmbeddingField, metadataField: config.MetadataField,
		embeddingClient: client, documentBatcher: config.DocumentBatcher, httpClient: config.HTTPClient, maxResponseBytes: config.MaxResponseBytes,
	}
	raw, err := store.sendJSON(ctx, http.MethodGet, "/indexes/"+url.PathEscape(store.indexName), nil)
	if err != nil {
		return nil, fmt.Errorf("azureaisearch: read native index: %w", err)
	}
	var schema indexSchema
	if err = jsonv2.Unmarshal(raw, &schema); err != nil {
		return nil, fmt.Errorf("azureaisearch: decode native index: %w", err)
	}
	store.metric, store.dimensions, err = schema.bind(store.idField, store.contentField, store.embeddingField, store.metadataField)
	if err != nil {
		return nil, err
	}
	if _, err = store.selectIDs(ctx, nil); err != nil {
		return nil, fmt.Errorf("azureaisearch: verify stored records: %w", err)
	}
	return store, nil
}

func (s *Store) validateVector(vector []float32) error {
	if len(vector) != s.dimensions {
		return fmt.Errorf("azureaisearch: native vector width is %d, got %d", s.dimensions, len(vector))
	}
	projected := make([]float64, len(vector))
	for i, value := range vector {
		projected[i] = float64(value)
	}
	_, err := embedding.NewOutput(projected, nil)
	return err
}

func (s *Store) decodeDocument(row metadata.Map) (*document.Document, error) {
	for field := range row {
		if field != s.idField && field != s.contentField && field != s.embeddingField && field != s.metadataField && !strings.HasPrefix(field, "@") {
			return nil, fmt.Errorf("azureaisearch: unexpected stored field %q", field)
		}
	}
	id, present, err := row.Decode[*string](s.idField)
	if err != nil || !present || id == nil {
		return nil, fmt.Errorf("azureaisearch: missing or invalid document key: %w", errors.Join(err, vectorstore.ErrInvalidDocument))
	}
	if err = validateID(*id); err != nil {
		return nil, err
	}
	text, present, err := row.Decode[*string](s.contentField)
	if err != nil || !present || text == nil {
		return nil, fmt.Errorf("azureaisearch: missing or invalid content: %w", errors.Join(err, vectorstore.ErrInvalidDocument))
	}
	encoded, present, err := row.Decode[*string](s.metadataField)
	if err != nil || !present || encoded == nil {
		return nil, fmt.Errorf("azureaisearch: missing or invalid Core metadata JSON: %w", errors.Join(err, vectorstore.ErrInvalidDocument))
	}
	var facts metadata.Map
	if err = facts.UnmarshalJSON([]byte(*encoded)); err != nil {
		return nil, fmt.Errorf("azureaisearch: decode Core metadata: %w", err)
	}
	vector, present, err := row.Decode[[]float32](s.embeddingField)
	if err != nil || !present {
		return nil, fmt.Errorf("azureaisearch: missing or invalid vector: %w", errors.Join(err, vectorstore.ErrInvalidDocument))
	}
	if err = s.validateVector(vector); err != nil {
		return nil, err
	}
	doc := &document.Document{ID: *id, Text: *text, Metadata: facts}
	if err = (&vectorstore.IndexRequest{Documents: []*document.Document{doc}}).Validate(); err != nil {
		return nil, err
	}
	return doc, nil
}

func matches(doc *document.Document, predicate filter.Predicate) (bool, error) {
	if predicate == nil {
		return true, nil
	}
	values, err := doc.Metadata.Values()
	if err != nil {
		return false, err
	}
	return filter.Match(predicate, values)
}

func (s *Store) selectIDs(ctx context.Context, predicate filter.Predicate) ([]string, error) {
	var ids []string
	seen := make(map[string]struct{})
	pageFilter := ""
	for {
		body := map[string]any{"select": s.selectedFields(), "top": maximumResultsPerPage, "orderby": s.idField + " asc"}
		if pageFilter != "" {
			body["filter"] = pageFilter
		}
		rows, _, err := s.searchPage(ctx, body)
		if err != nil {
			return nil, fmt.Errorf("azureaisearch: scan stored records: %w", err)
		}
		if len(rows) > maximumResultsPerPage {
			return nil, errors.New("azureaisearch: scan returned excess records")
		}
		if len(rows) == 0 {
			return ids, nil
		}
		last := ""
		for _, row := range rows {
			doc, decodeErr := s.decodeDocument(row)
			if decodeErr != nil {
				return nil, decodeErr
			}
			if _, duplicate := seen[doc.ID]; duplicate {
				return nil, fmt.Errorf("azureaisearch: keyset scan repeated document %q", doc.ID)
			}
			seen[doc.ID] = struct{}{}
			match, matchErr := matches(doc, predicate)
			if matchErr != nil {
				return nil, fmt.Errorf("azureaisearch: filter stored document %q: %w", doc.ID, matchErr)
			}
			if match {
				ids = append(ids, doc.ID)
			}
			last = doc.ID
		}
		// Key ranges, unlike skip offsets, do not depend on previous page positions.
		// Only an empty response establishes that the current walk has ended.
		pageFilter = s.idField + " gt '" + last + "'"
	}
}

func (s *Store) selectedFields() string {
	return strings.Join([]string{s.idField, s.contentField, s.embeddingField, s.metadataField}, ",")
}

func (s *Store) Index(ctx context.Context, request *vectorstore.IndexRequest) error {
	if err := request.Validate(); err != nil {
		return err
	}
	for i, doc := range request.Documents {
		if doc.Media != nil {
			return fmt.Errorf("azureaisearch: %w: documents[%d] contains unsupported media", vectorstore.ErrInvalidDocument, i)
		}
		if err := validateID(doc.ID); err != nil {
			return err
		}
	}
	batches, err := request.Batch(ctx, s.documentBatcher)
	if err != nil {
		return err
	}
	var actions []map[string]any
	for _, batch := range batches {
		texts, textErr := batch.Texts()
		if textErr != nil {
			return textErr
		}
		vectors, vectorErr := s.embeddingClient.EmbedTexts(ctx, texts)
		if vectorErr != nil {
			return vectorErr
		}
		for i, doc := range batch.Documents {
			encoded, encodeErr := doc.Metadata.MarshalJSON()
			if encodeErr != nil {
				return encodeErr
			}
			vector := embedding.Float32Vector(vectors[i])
			if err = s.validateVector(vector); err != nil {
				return err
			}
			actions = append(actions, map[string]any{"@search.action": "upload", s.idField: doc.ID, s.contentField: doc.Text, s.embeddingField: vector, s.metadataField: string(encoded)})
		}
	}
	return s.writeActions(ctx, actions)
}

type rankedResult struct {
	result   *vectorstore.SearchResult
	rawScore float64
}

func (s *Store) Search(ctx context.Context, request *vectorstore.SearchRequest) (response *vectorstore.SearchResponse, err error) {
	if err = request.Validate(); err != nil {
		return nil, err
	}
	if err = request.Options.RequireMode(vectorstore.SearchModeSemantic, vectorstore.SearchModeHybrid); err != nil {
		return nil, err
	}
	if request.Options.ResultLimit() > maximumResultsPerPage {
		return nil, fmt.Errorf("azureaisearch: %w: native top K cannot exceed %d", vectorstore.ErrInvalidOptions, maximumResultsPerPage)
	}
	defer func() {
		if err == nil {
			err = response.ValidateFor(request)
		}
		if err != nil {
			response = nil
		}
	}()
	ids, err := s.selectIDs(ctx, request.Options.Filter)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return &vectorstore.SearchResponse{}, nil
	}
	vector, err := s.embeddingClient.EmbedText(ctx, request.Query)
	if err != nil {
		return nil, err
	}
	queryVector := embedding.Float32Vector(vector)
	if err = s.validateVector(queryVector); err != nil {
		return nil, err
	}
	body := map[string]any{
		"count": false, "top": request.Options.ResultLimit(), "select": s.selectedFields(), "vectorFilterMode": "preFilter",
		"vectorQueries": []any{map[string]any{"kind": "vector", "vector": queryVector, "k": request.Options.ResultLimit(), "fields": s.embeddingField}},
		"filter":        "search.in(" + s.idField + ", '" + strings.Join(ids, ",") + "', ',')",
	}
	if request.Options.EffectiveMode() == vectorstore.SearchModeHybrid {
		body["search"] = request.Query
		body["searchFields"] = s.contentField
	}
	rows, err := s.searchDocuments(ctx, body)
	if err != nil {
		return nil, err
	}
	if len(rows) > min(len(ids), request.Options.ResultLimit()) {
		return nil, errors.New("azureaisearch: query returned excess records")
	}
	expected := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		expected[id] = struct{}{}
	}
	seen := make(map[string]struct{}, len(rows))
	ranked := make([]rankedResult, 0, len(rows))
	for _, row := range rows {
		doc, decodeErr := s.decodeDocument(row)
		if decodeErr != nil {
			return nil, decodeErr
		}
		if _, exists := expected[doc.ID]; !exists {
			return nil, fmt.Errorf("azureaisearch: query returned unexpected document %q", doc.ID)
		}
		if _, duplicate := seen[doc.ID]; duplicate {
			return nil, fmt.Errorf("azureaisearch: query repeated document %q", doc.ID)
		}
		seen[doc.ID] = struct{}{}
		match, matchErr := matches(doc, request.Options.Filter)
		if matchErr != nil {
			return nil, matchErr
		}
		rawScore, present, scoreErr := row.Decode[*float64]("@search.score")
		if scoreErr != nil || !present || rawScore == nil {
			return nil, fmt.Errorf("azureaisearch: missing or invalid native score: %w", errors.Join(scoreErr, vectorstore.ErrInvalidResponse))
		}
		score, scoreErr := s.metric.score(*rawScore, request.Options.EffectiveMode())
		if scoreErr != nil {
			return nil, scoreErr
		}
		if match {
			ranked = append(ranked, rankedResult{result: &vectorstore.SearchResult{Document: doc, Score: score}, rawScore: *rawScore})
		}
	}
	slices.SortFunc(ranked, func(left, right rankedResult) int {
		if order := cmp.Compare(right.rawScore, left.rawScore); order != 0 {
			return order
		}
		return strings.Compare(left.result.Document.ID, right.result.Document.ID)
	})
	results := make([]*vectorstore.SearchResult, 0, len(ranked))
	for _, row := range ranked {
		if row.result.Score >= request.Options.MinScore {
			results = append(results, row.result)
		}
	}
	return &vectorstore.SearchResponse{Results: results}, nil
}

func (s *Store) DeleteIDs(ctx context.Context, ids []string) error {
	seen := make(map[string]struct{}, len(ids))
	var actions []map[string]any
	for _, id := range ids {
		if err := validateID(id); err != nil {
			return err
		}
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		actions = append(actions, map[string]any{"@search.action": "delete", s.idField: id})
	}
	return s.writeActions(ctx, actions)
}

func (s *Store) searchPage(ctx context.Context, body any) ([]metadata.Map, map[string]json.RawMessage, error) {
	raw, err := s.sendJSON(ctx, http.MethodPost, "/indexes/"+url.PathEscape(s.indexName)+"/docs/search", body)
	if err != nil {
		return nil, nil, err
	}
	var page struct {
		Value          []metadata.Map             `json:"value"`
		NextParameters map[string]json.RawMessage `json:"@search.nextPageParameters"`
		NextLink       string                     `json:"@odata.nextLink"`
	}
	if err = jsonv2.Unmarshal(raw, &page); err != nil {
		return nil, nil, err
	}
	if page.Value == nil {
		return nil, nil, errors.New("azureaisearch: search response is missing its result array")
	}
	if len(page.NextParameters) == 0 && page.NextLink != "" {
		return nil, nil, errors.New("azureaisearch: search continuation is missing POST parameters")
	}
	return page.Value, page.NextParameters, nil
}

func (s *Store) searchDocuments(ctx context.Context, body any) ([]metadata.Map, error) {
	var rows []metadata.Map
	seen := make(map[string]struct{})
	for {
		encoded, err := jsonv2.Marshal(body, jsonv2.Deterministic(true))
		if err != nil {
			return nil, err
		}
		if _, repeated := seen[string(encoded)]; repeated {
			return nil, errors.New("azureaisearch: search continuation repeated its parameters")
		}
		seen[string(encoded)] = struct{}{}
		page, next, err := s.searchPage(ctx, body)
		if err != nil {
			return nil, err
		}
		rows = append(rows, page...)
		if len(next) == 0 {
			return rows, nil
		}
		// Server-provided POST parameters may advance paging, never the endpoint
		// to which the host's credentials are sent.
		body = next
	}
}

func (s *Store) writeActions(ctx context.Context, actions []map[string]any) error {
	prepared := make([]json.RawMessage, 0)
	for batch := range slices.Chunk(actions, maximumDocumentsPerBatch) {
		raw, err := jsonv2.Marshal(map[string]any{"value": batch})
		if err != nil {
			return err
		}
		if len(raw) > maximumRequestBytes {
			return errors.New("azureaisearch: write request exceeds the native 16 MiB limit")
		}
		prepared = append(prepared, raw)
	}
	path := "/indexes/" + url.PathEscape(s.indexName) + "/docs/index"
	offset := 0
	for _, body := range prepared {
		batch := actions[offset:min(offset+maximumDocumentsPerBatch, len(actions))]
		raw, err := s.sendJSON(ctx, http.MethodPost, path, body)
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
		if err = jsonv2.Unmarshal(raw, &response); err != nil {
			return err
		}
		if len(response.Value) != len(batch) {
			return fmt.Errorf("azureaisearch: write response contains %d results for %d actions", len(response.Value), len(batch))
		}
		pending := make(map[string]string, len(batch))
		for _, action := range batch {
			pending[action[s.idField].(string)] = action["@search.action"].(string)
		}
		for _, result := range response.Value {
			operation, expected := pending[result.Key]
			if !expected {
				return fmt.Errorf("azureaisearch: write response contains unknown or repeated key %q", result.Key)
			}
			delete(pending, result.Key)
			if !result.Status || (result.StatusCode != http.StatusOK && (operation != "upload" || result.StatusCode != http.StatusCreated)) {
				return fmt.Errorf("azureaisearch: document %q: status=%d: %s", result.Key, result.StatusCode, result.ErrorMessage)
			}
		}
		offset += len(batch)
	}
	return nil
}

func (s *Store) sendJSON(ctx context.Context, method, path string, body any) ([]byte, error) {
	var reqBody io.Reader
	if body != nil {
		encoded, err := jsonv2.Marshal(body)
		if err != nil {
			return nil, err
		}
		if len(encoded) > maximumRequestBytes {
			return nil, errors.New("azureaisearch: request exceeds the native 16 MiB limit")
		}
		reqBody = bytes.NewReader(encoded)
	}
	address := s.endpoint + path + "?api-version=" + url.QueryEscape(s.apiVersion)
	request, err := http.NewRequestWithContext(ctx, method, address, reqBody)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	response, err := s.httpClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	limit := s.maxResponseBytes
	raw, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, fmt.Errorf("azureaisearch: response exceeds %d-byte limit", limit)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("azureaisearch: status=%d body=%s", response.StatusCode, raw)
	}
	return raw, nil
}
