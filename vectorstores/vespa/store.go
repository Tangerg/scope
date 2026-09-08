package vespa

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

// Exported identifiers keep provider-owned names and defaults out of caller literals.
const (
	Provider = "Vespa"

	// DefaultContentField names the document field that stores the
	// raw text.
	DefaultContentField = "content"

	// DefaultEmbeddingField names the document field that stores the
	// vector tensor.
	DefaultEmbeddingField = "embedding"

	// DefaultIDField names the field used for the Scope document id.
	DefaultIDField = "doc_id"

	// DefaultQueryTensorName names the rank-profile query tensor.
	DefaultQueryTensorName = "q"

	DefaultMaxResponseBytes = int64(16 * 1024 * 1024)

	// Staying within Vespa's default maxHits avoids requiring a query-profile
	// override merely to enumerate documents for deletion.
	deletePageSize = 400

	// healthCodeUp is the one status code /state/v1/health reports for a
	// container that is fully operational; "initializing" and "down" are the
	// other two it documents.
	healthCodeUp = "up"
)

// ErrUnavailableContainer reports a Vespa container that answered its health
// endpoint without being ready to serve.
var ErrUnavailableContainer = errors.New("vespa: container is unavailable")

// StoreConfig contains configuration options for the Vespa vector
// store. Vespa uses an HTTP REST surface; the store assumes the
// schema (the .sd file) is provisioned out of band — Vespa schema
// management is YAML/SDL and lives in the application package.
type StoreConfig struct {
	// Endpoint is the Vespa container endpoint (Document API + search
	// API), e.g. "https://my-app.aws-us-east-1c.z.vespa-app.cloud" or
	// "http://localhost:8080". Required.
	Endpoint string

	// SchemaName is the document type name (matches the schema name
	// in the .sd file). Required.
	SchemaName string

	// Namespace is the document-id namespace component. Required by
	// the Vespa document-id grammar but commonly defaults to the
	// schema name.
	Namespace string

	// EmbeddingField / ContentField / IDField name the well-known
	// schema fields the store writes to. Optional defaults apply.
	EmbeddingField string
	ContentField   string
	IDField        string

	// QueryTensorName is the query tensor declared by RankingProfile. Optional:
	// defaults to [DefaultQueryTensorName].
	QueryTensorName string

	// RankingProfile is the Vespa rank profile used for nearest-neighbor
	// scoring. It must rank by closeness(field, <EmbeddingField>), whose
	// relevance is in [0, 1]. Required: Vespa's built-in default profile uses
	// nativeRank and does not represent vector similarity.
	RankingProfile string

	// EmbeddingModel produces vectors for the documents. Required.
	EmbeddingModel embedding.Model

	// DocumentBatcher batches documents before upload. Required.
	DocumentBatcher vectorstore.Batcher

	// HTTPClient lets callers override transport (timeouts,
	// proxies, mTLS for Vespa Cloud). Optional: defaults to
	// http.DefaultClient.
	HTTPClient *http.Client

	// MaxResponseBytes bounds every buffered HTTP response. Zero selects
	// [DefaultMaxResponseBytes].
	MaxResponseBytes int64
}

func (s StoreConfig) Validate() error {
	s.applyDefaults()
	if s.Endpoint == "" {
		return errors.New("vespa: Endpoint is required")
	}
	if s.SchemaName == "" {
		return errors.New("vespa: SchemaName is required")
	}
	if s.RankingProfile == "" {
		return errors.New("vespa: RankingProfile is required")
	}
	if lo.IsNil(s.EmbeddingModel) {
		return errors.New("vespa: EmbeddingModel is required")
	}
	if lo.IsNil(s.DocumentBatcher) {
		return errors.New("vespa: DocumentBatcher is required")
	}
	if s.MaxResponseBytes < 0 {
		return errors.New("vespa: MaxResponseBytes must not be negative")
	}
	return s.validateIdentifiers()
}

func (s StoreConfig) validateIdentifiers() error {
	if err := identifier(s.SchemaName).validate("SchemaName"); err != nil {
		return err
	}
	if err := identifier(s.Namespace).validate("Namespace"); err != nil {
		return err
	}
	if err := identifier(s.EmbeddingField).validate("EmbeddingField"); err != nil {
		return err
	}
	if err := identifier(s.ContentField).validate("ContentField"); err != nil {
		return err
	}
	if err := identifier(s.IDField).validate("IDField"); err != nil {
		return err
	}
	if err := identifier(s.QueryTensorName).validate("QueryTensorName"); err != nil {
		return err
	}
	if err := identifier(s.RankingProfile).validate("RankingProfile"); err != nil {
		return err
	}
	return nil
}

// applyDefaults fills zero fields with documented defaults.
func (s *StoreConfig) applyDefaults() {
	if s.Namespace == "" {
		s.Namespace = s.SchemaName
	}
	s.EmbeddingField = cmp.Or(s.EmbeddingField, DefaultEmbeddingField)
	s.ContentField = cmp.Or(s.ContentField, DefaultContentField)
	s.IDField = cmp.Or(s.IDField, DefaultIDField)
	s.QueryTensorName = cmp.Or(s.QueryTensorName, DefaultQueryTensorName)
	if s.HTTPClient == nil {
		s.HTTPClient = http.DefaultClient
	}
}

var (
	_ vectorstore.Indexer       = (*Store)(nil)
	_ vectorstore.Searcher      = (*Store)(nil)
	_ vectorstore.FilterDeleter = (*Store)(nil)
)

// Store implements vector-store capabilities through Vespa's REST API.
type Store struct {
	endpoint         string
	schemaName       string
	namespace        string
	embeddingField   string
	contentField     string
	idField          string
	queryTensorName  string
	rankingProfile   string
	embeddingClient  embeddingclient.Client
	documentBatcher  vectorstore.Batcher
	httpClient       *http.Client
	maxResponseBytes int64
}

// NewStore needs no context because construction performs no I/O; the schema is
// provisioned outside this package, so the store only validates configuration
// and assembles state.
func NewStore(ctx context.Context, config StoreConfig) (*Store, error) {
	config.applyDefaults()
	if err := config.Validate(); err != nil {
		return nil, err
	}
	embeddingClient, err := embeddingclient.New(config.EmbeddingModel)
	if err != nil {
		return nil, fmt.Errorf("vespa: create embedding client: %w", err)
	}
	store := &Store{
		endpoint:         strings.TrimRight(config.Endpoint, "/"),
		schemaName:       config.SchemaName,
		namespace:        config.Namespace,
		embeddingField:   config.EmbeddingField,
		contentField:     config.ContentField,
		idField:          config.IDField,
		queryTensorName:  config.QueryTensorName,
		rankingProfile:   config.RankingProfile,
		embeddingClient:  embeddingClient,
		documentBatcher:  config.DocumentBatcher,
		httpClient:       config.HTTPClient,
		maxResponseBytes: cmp.Or(config.MaxResponseBytes, DefaultMaxResponseBytes),
	}
	if err = store.verifyContainer(ctx); err != nil {
		return nil, err
	}
	return store, nil
}

// verifyContainer reads the container's own health endpoint.
//
// The schema and rank profile live in an application package this store cannot
// read, so there is nothing here to compare a configured metric against. What
// the container does report is whether it can answer at all: "up" is fully
// operational, while "initializing" means a container with the query API is
// still "waiting for content nodes to start", and a store built against one of
// those would fail every request for a reason that has nothing to do with the
// caller's query. A wrong endpoint surfaces here as well, at the wiring that
// names it.
func (s *Store) verifyContainer(ctx context.Context) error {
	raw, err := s.sendJSON(ctx, http.MethodGet, "/state/v1/health", nil)
	if err != nil {
		return fmt.Errorf("vespa: read container health at %s: %w", s.endpoint, err)
	}
	var health struct {
		Status struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"status"`
	}
	if err = json.Unmarshal(raw, &health); err != nil {
		return fmt.Errorf("vespa: decode container health: %w", err)
	}
	if health.Status.Code != healthCodeUp {
		return fmt.Errorf("%w: container at %s reports %q: %s",
			ErrUnavailableContainer, s.endpoint, health.Status.Code, health.Status.Message)
	}
	return nil
}

// Index embeds documents and writes them through the Vespa Document API.
func (s *Store) Index(ctx context.Context, request *vectorstore.IndexRequest) (err error) {
	if validateErr := request.Validate(); validateErr != nil {
		return fmt.Errorf("vespa.Store.Index: %w", validateErr)
	}

	var batches []*vectorstore.IndexRequest
	batches, err = request.Batch(ctx, s.documentBatcher)
	if err != nil {
		return fmt.Errorf("vespa: batch documents: %w", err)
	}

	for _, batch := range batches {
		docs := batch.Documents
		texts, err := batch.Texts()
		if err != nil {
			return fmt.Errorf("vectorstore: project document text: %w", err)
		}
		vectors, err := s.embeddingClient.EmbedTexts(ctx, texts)
		if err != nil {
			return fmt.Errorf("vespa: embed documents: %w", err)
		}
		for i, doc := range docs {
			id := doc.ID
			fields := map[string]any{
				s.idField:        id,
				s.contentField:   doc.Text,
				s.embeddingField: map[string]any{"values": embedding.Float32Vector(vectors[i])},
			}
			for k, v := range doc.Metadata {
				fields[k] = v
			}
			body := map[string]any{"fields": fields}
			path := fmt.Sprintf("/document/v1/%s/%s/docid/%s",
				url.PathEscape(s.namespace), url.PathEscape(s.schemaName), url.PathEscape(id))
			if _, err := s.sendJSON(ctx, http.MethodPost, path, body); err != nil {
				return fmt.Errorf("vespa: index document %q: %w", id, err)
			}
		}
	}
	return nil
}

// Search runs a nearestNeighbor YQL query.
func (s *Store) Search(ctx context.Context, req *vectorstore.SearchRequest) (response *vectorstore.SearchResponse, err error) {
	var docs []*vectorstore.SearchResult
	if err = req.Validate(); err != nil {
		return nil, fmt.Errorf("vespa.Store.Search: %w", err)
	}
	if err = req.Options.RequireMode(vectorstore.SearchModeSemantic); err != nil {
		return nil, fmt.Errorf("vespa.Store.Search: %w", err)
	}

	defer func() {
		if err == nil {
			err = response.ValidateFor(req)
		}
	}()

	vector, err := s.embeddingClient.EmbedText(ctx, req.Query)
	if err != nil {
		return nil, fmt.Errorf("vespa: embed query: %w", err)
	}
	queryVec := embedding.Float32Vector(vector)

	filterFragment, err := s.buildFilter(req.Options.Filter)
	if err != nil {
		return nil, err
	}

	nn := fmt.Sprintf("{targetHits:%d}nearestNeighbor(%s, %s)",
		req.Options.ResultLimit(), s.embeddingField, s.queryTensorName)
	yql := fmt.Sprintf("select * from %s where %s", s.schemaName, nn)
	if filterFragment != "" {
		yql = yql + " and " + filterFragment
	}

	body := map[string]any{
		"yql":  yql,
		"hits": req.Options.ResultLimit(),
		fmt.Sprintf("input.query(%s)", s.queryTensorName): map[string]any{"values": queryVec},
		"ranking": s.rankingProfile,
	}

	hits, err := s.query(ctx, body)
	if err != nil {
		return nil, fmt.Errorf("vespa: search: %w", err)
	}

	docs = make([]*vectorstore.SearchResult, 0, len(hits))
	for _, hit := range hits {
		if hit.Relevance == nil {
			return nil, errors.New("vespa: search hit is missing relevance")
		}
		// Vespa relevance for nearestNeighbor is the configured
		// distance metric's similarity directly (cosine: [0, 1]).
		score := vectorstore.ScoreFromValue(*hit.Relevance)
		if score < req.Options.MinScore {
			continue
		}
		doc, err := s.toDocument(hit.ID, hit.Fields)
		if err != nil {
			return nil, err
		}
		docs = append(docs, &vectorstore.SearchResult{Document: doc, Score: score})
	}
	return &vectorstore.SearchResponse{Results: docs}, nil
}

func (s *Store) DeleteWhere(ctx context.Context, expr filter.Predicate) (err error) {
	if expr == nil {
		return vectorstore.ErrMissingFilter
	}
	if err = expr.Validate(); err != nil {
		return fmt.Errorf("vespa.Store.DeleteWhere: %w", err)
	}

	filterFragment, err := s.buildFilter(expr)
	if err != nil {
		return err
	}
	if filterFragment == "" {
		return errors.New("vespa: refusing to delete on empty filter")
	}

	for {
		yql := fmt.Sprintf("select %s from %s where %s",
			s.idField, s.schemaName, filterFragment)
		body := map[string]any{
			"yql":  yql,
			"hits": deletePageSize,
		}
		hits, err := s.query(ctx, body)
		if err != nil {
			return fmt.Errorf("vespa: enumerate ids: %w", err)
		}
		if len(hits) == 0 {
			return nil
		}
		for _, hit := range hits {
			id, present, err := hit.Fields.Decode[string](s.idField)
			if err != nil {
				return fmt.Errorf("vespa: enumerate ids: decode field %q: %w", s.idField, err)
			}
			if !present || id == "" {
				return fmt.Errorf("vespa: enumerate ids: search hit is missing string field %q", s.idField)
			}
			path := fmt.Sprintf("/document/v1/%s/%s/docid/%s",
				url.PathEscape(s.namespace), url.PathEscape(s.schemaName), url.PathEscape(id))
			if _, err := s.sendJSON(ctx, http.MethodDelete, path, nil); err != nil {
				return fmt.Errorf("vespa: delete %s: %w", id, err)
			}
		}
	}
}

// queryHit is one Vespa search hit. Relevance is absent for queries that
// project fields without ranking, so only ranked callers require it.
type queryHit struct {
	ID        string       `json:"id"`
	Relevance *float64     `json:"relevance"`
	Fields    metadata.Map `json:"fields"`
}

type queryResponse struct {
	Root struct {
		Errors   []queryError   `json:"errors"`
		Coverage *queryCoverage `json:"coverage"`
		Children []queryHit     `json:"children"`
	} `json:"root"`
}

type queryError struct {
	Code    int    `json:"code"`
	Summary string `json:"summary"`
	Message string `json:"message"`
}

// queryCoverage reports how much of the corpus a query actually evaluated.
// Degraded holds one flag per degradation cause and is decoded as a map so a
// newly introduced cause still reaches the caller.
type queryCoverage struct {
	Coverage int             `json:"coverage"`
	Full     bool            `json:"full"`
	Degraded map[string]bool `json:"degraded"`
}

func (q queryCoverage) reasons() string {
	causes := make([]string, 0, len(q.Degraded))
	for cause, degraded := range q.Degraded {
		if degraded {
			causes = append(causes, cause)
		}
	}
	if len(causes) == 0 {
		return "unspecified"
	}
	slices.Sort(causes)
	return strings.Join(causes, ",")
}

// query runs one Vespa query and returns its hits only when the response proves
// the query was fully evaluated. Soft timeout is enabled by default, so Vespa
// answers a partially evaluated query with 200 and a degraded coverage report
// rather than an error status; treating those hits as complete would silently
// shrink a search result and silently skip documents during filtered deletion.
func (s *Store) query(ctx context.Context, body map[string]any) ([]queryHit, error) {
	raw, err := s.sendJSON(ctx, http.MethodPost, "/search/", body)
	if err != nil {
		return nil, err
	}
	var parsed queryResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("decode query response: %w", err)
	}
	if len(parsed.Root.Errors) > 0 {
		reported := parsed.Root.Errors[0]
		return nil, fmt.Errorf("query reported error %d: %s: %s",
			reported.Code, reported.Summary, reported.Message)
	}
	if parsed.Root.Coverage == nil {
		return nil, errors.New("query response is missing its coverage report")
	}
	if !parsed.Root.Coverage.Full {
		return nil, fmt.Errorf("query evaluated %d%% of the corpus, degraded by %s",
			parsed.Root.Coverage.Coverage, parsed.Root.Coverage.reasons())
	}
	return parsed.Root.Children, nil
}

func (s *Store) buildFilter(expr filter.Predicate) (string, error) {
	if expr == nil {
		return "", nil
	}
	v := newVisitor("")
	if err := expr.Accept(v); err != nil {
		return "", fmt.Errorf("vespa: convert filter: %w", err)
	}
	return v.snapshot(), nil
}

func (s *Store) toDocument(rawID string, fields metadata.Map) (*document.Document, error) {
	doc := &document.Document{}
	id, present, err := fields.Decode[string](s.idField)
	if err != nil {
		return nil, fmt.Errorf("vespa: decode field %q: %w", s.idField, err)
	}
	if present && id != "" {
		doc.ID = id
	} else {
		// Fall back to the Vespa-native id like "id:namespace:schema::docid".
		if idx := strings.LastIndex(rawID, "::"); idx > 0 {
			doc.ID = rawID[idx+2:]
		} else {
			doc.ID = rawID
		}
	}
	if doc.ID == "" {
		return nil, errors.New("vespa: search hit has no stable document ID")
	}
	text, present, err := fields.Decode[string](s.contentField)
	if err != nil {
		return nil, fmt.Errorf("vespa: decode field %q: %w", s.contentField, err)
	}
	if !present || text == "" {
		return nil, fmt.Errorf("vespa: document %q is missing string field %q", doc.ID, s.contentField)
	}
	doc.Text = text

	// Carry the remaining fields as raw JSON so a stored integer beyond the
	// exact float64 range reaches the caller unchanged.
	meta := make(metadata.Map, len(fields))
	for key, value := range fields {
		switch key {
		case s.idField, s.contentField, s.embeddingField:
			continue
		}
		meta[key] = value
	}
	if len(meta) > 0 {
		doc.Metadata = meta
	}
	return doc, nil
}

func (s *Store) sendJSON(ctx context.Context, method, path string, body any) ([]byte, error) {
	u := s.endpoint + path

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
