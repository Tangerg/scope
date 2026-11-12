package azureaisearch

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
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

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/httpclient"
	"github.com/Tangerg/scope/core/metadata"
	corevector "github.com/Tangerg/scope/core/vector"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

const (
	Provider                 = "AzureAISearch"
	DefaultAPIVersion        = "2024-07-01"
	DefaultIDField           = "id"
	DefaultContentField      = "content"
	DefaultMetadataField     = "scope_metadata"
	DefaultMaxResponseBytes  = int64(16 * 1024 * 1024)
	maximumDocumentsPerBatch = 1000
	maximumResultsPerPage    = 1000
	maximumRequestBytes      = 16 * 1024 * 1024
)

// StoreConfig binds an existing index. The host owns schema creation,
// authentication, transport timeouts, retries and the HTTP client's lifecycle.
type StoreConfig struct {
	Endpoint      string
	IndexName     string
	APIVersion    string
	IDField       string
	ContentField  string
	MetadataField string
	Space         vectorstore.SpaceID
	// HTTPClient settings are captured; the host retains its transport and jar.
	// Requests remain within Endpoint's origin even when redirects are followed.
	HTTPClient       *http.Client
	MaxResponseBytes int64
}

func (s *StoreConfig) applyDefaults() {
	s.APIVersion = cmp.Or(s.APIVersion, DefaultAPIVersion)
	s.IDField = cmp.Or(s.IDField, DefaultIDField)
	s.ContentField = cmp.Or(s.ContentField, DefaultContentField)
	s.MetadataField = cmp.Or(s.MetadataField, DefaultMetadataField)
	s.MaxResponseBytes = cmp.Or(s.MaxResponseBytes, DefaultMaxResponseBytes)
}

func (s StoreConfig) Validate() error {
	s.applyDefaults()
	if _, err := httpclient.ParseOrigin(s.Endpoint); err != nil {
		return errors.New("azureaisearch: Endpoint must be an HTTP service origin")
	}
	if !validIndexName(s.IndexName) {
		return errors.New("azureaisearch: invalid native index name")
	}
	if s.HTTPClient == nil {
		return errors.New("azureaisearch: HTTPClient is required")
	}
	if err := s.Space.Validate(); err != nil {
		return err
	}
	if s.MaxResponseBytes < 0 || s.MaxResponseBytes == math.MaxInt64 {
		return errors.New("azureaisearch: MaxResponseBytes must be nonnegative and below MaxInt64")
	}
	seen := make(map[string]struct{}, 4)
	for _, field := range []string{s.IDField, s.ContentField, s.Space.StorageKey(), s.MetadataField} {
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
	_ vectorstore.Upserter  = (*Store)(nil)
	_ vectorstore.Querier   = (*Store)(nil)
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
	space            vectorstore.SpaceID
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
	ownedHTTP, err := httpclient.New(ctx, httpclient.Config{HTTPClient: config.HTTPClient, BaseURL: config.Endpoint})
	if err != nil {
		return nil, err
	}
	store := &Store{
		endpoint: strings.TrimRight(config.Endpoint, "/"), indexName: config.IndexName, apiVersion: config.APIVersion,
		idField: config.IDField, contentField: config.ContentField, embeddingField: config.Space.StorageKey(), metadataField: config.MetadataField,
		space: config.Space, httpClient: ownedHTTP, maxResponseBytes: config.MaxResponseBytes,
	}
	store.metric, store.dimensions, err = store.binding(ctx)
	if err != nil {
		return nil, err
	}
	return store, nil
}

func (s *Store) binding(ctx context.Context) (nativeMetric, int, error) {
	raw, err := s.sendJSON(ctx, http.MethodGet, "/indexes/"+httpclient.PathSegment(s.indexName), nil)
	if err != nil {
		return "", 0, fmt.Errorf("azureaisearch: read native index: %w", err)
	}
	var schema indexSchema
	if err = jsonv2.Unmarshal(raw, &schema); err != nil {
		return "", 0, fmt.Errorf("azureaisearch: decode native index: %w", err)
	}
	metric, width, err := schema.bind(s.idField, s.contentField, s.embeddingField, s.metadataField)
	if err != nil {
		return "", 0, errors.Join(vectorstore.ErrIncompatibleSpace, err)
	}
	return metric, width, nil
}

func (s *Store) verify(ctx context.Context) error {
	metric, width, err := s.binding(ctx)
	if err != nil {
		return err
	}
	if metric != s.metric || width != s.dimensions {
		return vectorstore.ErrIncompatibleSpace
	}
	return nil
}

func (s *Store) coordinates(values []float64) ([]float32, error) {
	if len(values) != s.dimensions {
		return nil, fmt.Errorf("azureaisearch: %w: expected %d coordinates", vectorstore.ErrIncompatibleSpace, s.dimensions)
	}
	native := corevector.Float32(values)
	if err := s.validateVector(native); err != nil {
		return nil, err
	}
	return native, nil
}

func (s *Store) validateVector(vector []float32) error {
	if err := corevector.Coordinates[float32](vector).Validate(); err != nil {
		return fmt.Errorf("azureaisearch: %w: %w", vectorstore.ErrInvalidRequest, err)
	}
	if len(vector) != s.dimensions {
		return fmt.Errorf("azureaisearch: native vector width is %d, got %d", s.dimensions, len(vector))
	}
	return nil
}

func (s *Store) decodeDocument(row metadata.Map) (*document.Document, error) {
	for field := range row {
		if field != s.idField && field != s.contentField && field != s.embeddingField && field != s.metadataField && !strings.HasPrefix(field, searchAnnotationPrefix) {
			return nil, fmt.Errorf("azureaisearch: unexpected stored field %q", field)
		}
	}
	id, err := requiredString(row, s.idField, "document key")
	if err != nil {
		return nil, err
	}
	if err = validateID(id); err != nil {
		return nil, err
	}
	text, err := requiredString(row, s.contentField, "content")
	if err != nil {
		return nil, err
	}
	encoded, err := requiredString(row, s.metadataField, "Core metadata JSON")
	if err != nil {
		return nil, err
	}
	doc := new(document.Document)
	if err = doc.UnmarshalStoredMetadata([]byte(encoded)); err != nil {
		return nil, fmt.Errorf("azureaisearch: decode Core metadata: %w", err)
	}
	vector, present, err := row.Decode[corevector.Coordinates[float32]](s.embeddingField)
	if err != nil || !present {
		return nil, fmt.Errorf("azureaisearch: missing or invalid vector: %w", errors.Join(err, vectorstore.ErrInvalidDocument))
	}
	if err = s.validateVector(vector); err != nil {
		return nil, err
	}
	doc.ID, doc.Text = id, text
	if err = vectorstore.ValidateDocument(doc); err != nil {
		return nil, err
	}
	return doc, nil
}

// requiredString reads a stored string field, which must be present and not null.
func requiredString(row metadata.Map, field, label string) (string, error) {
	value, present, err := row.Decode[*string](field)
	if err != nil || !present || value == nil {
		return "", fmt.Errorf("azureaisearch: missing or invalid %s: %w", label, errors.Join(err, vectorstore.ErrInvalidDocument))
	}
	return *value, nil
}

func matches(doc *document.Document, predicate filter.Predicate) (bool, error) {
	if predicate == nil {
		return true, nil
	}
	return filter.Match(predicate, doc.Metadata)
}

func (s *Store) selectedFields() string {
	return strings.Join([]string{s.idField, s.contentField, s.embeddingField, s.metadataField}, ",")
}

func (s *Store) CheckDocuments(ctx context.Context, space vectorstore.SpaceID, docs []*document.Document) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := space.Require(s.space); err != nil {
		return err
	}
	if err := vectorstore.ValidateDocuments(docs); err != nil {
		return err
	}
	for _, doc := range docs {
		if doc.Media != nil {
			return fmt.Errorf("azureaisearch: %w: media unsupported", vectorstore.ErrInvalidDocument)
		}
		if err := validateID(doc.ID); err != nil {
			return err
		}
	}
	return s.verify(ctx)
}

func (s *Store) Upsert(ctx context.Context, batch *vectorstore.Batch) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := batch.ValidateFor(s.space); err != nil {
		return err
	}
	actions := make([]indexingAction, 0, len(batch.Records))
	for _, record := range batch.Records {
		doc := record.Document
		if doc.Media != nil {
			return fmt.Errorf("azureaisearch: %w: media unsupported", vectorstore.ErrInvalidDocument)
		}
		if err := validateID(doc.ID); err != nil {
			return err
		}
		raw, err := doc.MarshalStoredMetadata()
		if err != nil {
			return err
		}
		coords, err := s.coordinates(record.Values)
		if err != nil {
			return err
		}
		actions = append(actions, indexingAction{operation: actionUpload, id: doc.ID, fields: map[string]any{s.contentField: doc.Text, s.embeddingField: coords, s.metadataField: string(raw)}})
	}
	return s.writeActions(ctx, actions)
}

func (s *Store) CheckQuery(ctx context.Context, space vectorstore.SpaceID, opts vectorstore.SearchOptions) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := space.Require(s.space); err != nil {
		return err
	}
	if err := opts.Validate(); err != nil {
		return err
	}
	if err := opts.RequireMode(vectorstore.SearchModeSemantic, vectorstore.SearchModeHybrid); err != nil {
		return err
	}
	if opts.ResultLimit() > maximumResultsPerPage {
		return fmt.Errorf("azureaisearch: %w: native top K cannot exceed %d", vectorstore.ErrInvalidOptions, maximumResultsPerPage)
	}
	if opts.Filter != nil {
		return vectorstore.ErrUnsupportedFilter
	}
	return s.verify(ctx)
}

type rankedResult struct {
	result   *vectorstore.SearchResult
	rawScore float64
}

func (s *Store) Query(ctx context.Context, request *vectorstore.Query) (response *vectorstore.SearchResponse, err error) {
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if err = request.ValidateFor(s.space); err != nil {
		return nil, err
	}
	if err = s.CheckQuery(ctx, request.Space, request.Options); err != nil {
		return nil, err
	}
	queryVector, err := s.coordinates(request.Values)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err == nil {
			err = request.ValidateResponse(response)
		}
		if err != nil {
			response = nil
		}
	}()
	body := searchRequest{
		Count: new(false), Top: request.Options.ResultLimit(), Select: s.selectedFields(),
		VectorQueries: []vectorQuery{{Kind: queryKindVector, Vector: queryVector, K: request.Options.ResultLimit(), Fields: s.embeddingField}},
	}
	if request.Options.Mode == vectorstore.SearchModeHybrid {
		body.Search = request.LexicalText
		body.SearchFields = s.contentField
	}
	rows, err := s.searchDocuments(ctx, body, request.Options.ResultLimit())
	if err != nil {
		return nil, err
	}
	ranked, err := s.rankRows(rows, request.Options)
	if err != nil {
		return nil, err
	}
	results := make([]*vectorstore.SearchResult, 0, len(ranked))
	for _, row := range ranked {
		if row.result.Score >= request.Options.MinScore {
			results = append(results, row.result)
		}
	}
	return &vectorstore.SearchResponse{Results: results}, nil
}

// rankRows decodes the service's rows best first, rejecting more rows than
// requested, a repeated document, or a row without its native score.
func (s *Store) rankRows(rows []metadata.Map, options vectorstore.SearchOptions) ([]rankedResult, error) {
	if len(rows) > options.ResultLimit() {
		return nil, errors.New("azureaisearch: query returned excess records")
	}
	seen := make(map[string]struct{}, len(rows))
	ranked := make([]rankedResult, 0, len(rows))
	for _, row := range rows {
		doc, err := s.decodeDocument(row)
		if err != nil {
			return nil, err
		}
		if _, duplicate := seen[doc.ID]; duplicate {
			return nil, fmt.Errorf("azureaisearch: query repeated document %q", doc.ID)
		}
		seen[doc.ID] = struct{}{}
		rawScore, present, err := row.Decode[*float64](searchScoreField)
		if err != nil || !present || rawScore == nil {
			return nil, fmt.Errorf("azureaisearch: missing or invalid native score: %w", errors.Join(err, vectorstore.ErrInvalidResponse))
		}
		score, err := s.metric.score(*rawScore, options.Mode)
		if err != nil {
			return nil, err
		}
		result, err := vectorstore.NewSearchResult(doc, score)
		if err != nil {
			return nil, err
		}
		ranked = append(ranked, rankedResult{result: result, rawScore: *rawScore})
	}
	slices.SortFunc(ranked, func(left, right rankedResult) int {
		return cmp.Or(cmp.Compare(right.rawScore, left.rawScore), strings.Compare(left.result.Document.ID, right.result.Document.ID))
	})
	return ranked, nil
}

func (s *Store) DeleteIDs(ctx context.Context, ids []string) error {
	ids, err := vectorstore.DistinctIDs(ids)
	if err != nil {
		return err
	}
	var actions []indexingAction
	for _, id := range ids {
		if err := validateID(id); err != nil {
			return err
		}
		actions = append(actions, indexingAction{operation: actionDelete, id: id})
	}
	return s.writeActions(ctx, actions)
}

func (s *Store) searchPage(ctx context.Context, body any) ([]metadata.Map, map[string]json.RawMessage, error) {
	raw, err := s.sendJSON(ctx, http.MethodPost, "/indexes/"+httpclient.PathSegment(s.indexName)+"/docs/search", body)
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

func (s *Store) searchDocuments(ctx context.Context, body any, limit int) ([]metadata.Map, error) {
	var rows []metadata.Map
	seen := make(map[[sha256.Size]byte]struct{})
	for {
		encoded, err := jsonv2.Marshal(body, jsonv2.Deterministic(true))
		if err != nil {
			return nil, err
		}
		key := sha256.Sum256(encoded)
		if len(seen) > maximumResultsPerPage {
			return nil, errors.New("azureaisearch: search continuation exceeded the per-query page limit")
		}
		if _, repeated := seen[key]; repeated {
			return nil, errors.New("azureaisearch: search continuation repeated its parameters")
		}
		seen[key] = struct{}{}
		page, next, err := s.searchPage(ctx, body)
		if err != nil {
			return nil, err
		}
		if len(page) > limit-len(rows) {
			return nil, errors.New("azureaisearch: service exceeded requested result count")
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

func (s *Store) writeActions(ctx context.Context, actions []indexingAction) error {
	if len(actions) == 0 {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	prepared := make([]json.RawMessage, 0)
	for batch := range slices.Chunk(actions, maximumDocumentsPerBatch) {
		wire := make([]map[string]any, len(batch))
		for i, action := range batch {
			wire[i] = action.wire(s.idField)
		}
		raw, err := jsonv2.Marshal(indexingRequest{Value: wire})
		if err != nil {
			return err
		}
		if len(raw) > maximumRequestBytes {
			return errors.New("azureaisearch: write request exceeds the native 16 MiB limit")
		}
		prepared = append(prepared, raw)
	}
	if err := s.verify(ctx); err != nil {
		return err
	}
	path := "/indexes/" + httpclient.PathSegment(s.indexName) + "/docs/index"
	ids := actionIDs(actions)
	for offset, group := range prepared {
		start := offset * maximumDocumentsPerBatch
		end := min(start+maximumDocumentsPerBatch, len(actions))
		if err := ctx.Err(); err != nil {
			return vectorstore.NewWriteError(err, vectorstore.WriteOutcome{Acknowledged: ids[:start], Unattempted: ids[start:]})
		}
		raw, err := s.sendJSON(ctx, http.MethodPost, path, group)
		uncertain := func(cause error) error {
			return vectorstore.NewWriteError(cause, vectorstore.WriteOutcome{Acknowledged: ids[:start], Unconfirmed: ids[start:end], Unattempted: ids[end:]})
		}
		if err != nil {
			return uncertain(err)
		}
		outcome, err := readIndexingEvidence(raw, actions[start:end])
		if err != nil {
			outcome.Acknowledged = slices.Concat(ids[:start], outcome.Acknowledged)
			outcome.Unattempted = ids[end:]
			return vectorstore.NewWriteError(err, outcome)
		}
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
		return nil, fmt.Errorf("azureaisearch: %s: %w", method, httpclient.NewResponseError(response.StatusCode, response.Header))
	}
	return raw, nil
}

// Scan reads by increasing native key. MaxBytes counts canonical returned JSON
// rows, including vectors and metadata, even for rows the predicate rejects.
// Index changes between requests are visible; this is not a snapshot.
func (s *Store) Scan(ctx context.Context, request vectorstore.ScanRequest) ([]*document.Document, error) {
	if err := request.Validate(); err != nil {
		return nil, err
	}
	meter, err := request.Budget.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer meter.Close()
	ctx = meter.Context()
	if err := s.verify(ctx); err != nil {
		return nil, errors.Join(err, context.Cause(ctx))
	}
	var found []*document.Document
	count := 0
	last := ""
	for {
		if err := ctx.Err(); err != nil {
			return nil, errors.Join(err, context.Cause(ctx))
		}
		// One row past the grant proves that the source exceeds it.
		limit := min(maximumResultsPerPage, request.Budget.MaxRecords-count+1)
		body := searchRequest{Select: s.selectedFields(), Top: limit, OrderBy: s.idField + " asc"}
		if last != "" {
			body.Filter = s.idField + " gt '" + last + "'"
		}
		rows, err := s.searchDocuments(ctx, body, limit)
		if err != nil {
			return nil, errors.Join(err, context.Cause(ctx))
		}
		if len(rows) == 0 {
			break
		}
		if err := meter.ChargePage(len(rows), 0); err != nil {
			return nil, err
		}
		count += len(rows)
		for _, row := range rows {
			doc, err := s.admitScanned(meter, row, last)
			if err != nil {
				return nil, err
			}
			last = doc.ID
			ok, err := matches(doc, request.Filter)
			if err != nil {
				return nil, err
			}
			if ok {
				found = append(found, doc)
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, errors.Join(err, context.Cause(ctx))
	}
	return found, nil
}

// admitScanned charges a row's canonical wire bytes before decoding it and
// requires keys to advance past the previous one.
func (s *Store) admitScanned(meter *vectorstore.ScanMeter, row metadata.Map, last string) (*document.Document, error) {
	wire, err := jsonv2.Marshal(row, jsonv2.Deterministic(true))
	if err != nil {
		return nil, err
	}
	if err = meter.ChargePage(0, int64(len(wire))); err != nil {
		return nil, err
	}
	doc, err := s.decodeDocument(row)
	if err != nil {
		return nil, err
	}
	if last != "" && doc.ID <= last {
		return nil, fmt.Errorf("azureaisearch: non-advancing scan key %q", doc.ID)
	}
	return doc, nil
}
