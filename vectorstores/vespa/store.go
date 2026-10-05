package vespa

import (
	"bytes"
	"cmp"
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
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

	// DefaultQueryTensorName names the rank-profile query tensor.
	DefaultQueryTensorName = "q"

	DefaultMaxResponseBytes = int64(16 * 1024 * 1024)

	// DefaultMaxHits is the value Vespa documents for the query profile's
	// maxHits: "hits is capped at maxHits, default 400". The cap is applied
	// silently, so a search asking for more would come back short with nothing
	// to say it had been truncated.
	DefaultMaxHits = 400
)

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

	// EmbeddingField and ContentField name distinct schema fields that the
	// store writes to. Neither may name a native summary field or scope_namespace.
	// Optional defaults apply.
	EmbeddingField string
	ContentField   string

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

	// MaxHits is the query profile's maxHits for this application. Optional:
	// defaults to Vespa's own [DefaultMaxHits]. It belongs here for the same
	// reason SchemaName and RankingProfile do — it is a fact about the deployed
	// application package that this store cannot read, and Vespa caps hits
	// against it without saying so.
	MaxHits int
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
	if s.MaxHits < 0 {
		return errors.New("vespa: MaxHits must not be negative")
	}
	return s.validateIdentifiers()
}

func (s StoreConfig) validateIdentifiers() error {
	if err := (schemaFields{content: s.ContentField, embedding: s.EmbeddingField}).validate(); err != nil {
		return err
	}
	if err := identifier(s.SchemaName).validate("SchemaName"); err != nil {
		return err
	}
	if err := identifier(s.Namespace).validate("Namespace"); err != nil {
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
	s.QueryTensorName = cmp.Or(s.QueryTensorName, DefaultQueryTensorName)
	if s.HTTPClient == nil {
		s.HTTPClient = http.DefaultClient
	}
}

const defaultSummary = "default"

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
	fields           schemaFields
	queryTensorName  string
	rankingProfile   string
	embeddingClient  embeddingclient.Client
	documentBatcher  vectorstore.Batcher
	httpClient       *http.Client
	maxResponseBytes int64
	maxHits          int
}

// NewStore performs no I/O. Everything this store depends on — the schema, the
// fields, the rank profile's distance function — lives in an application
// package deployed out of band, and Vespa's documentation does not establish
// that a status path answers on the container endpoint this store is
// configured with, so there is nothing here it can confirm without guessing.
//
// The context is still taken, because every store in this family is
// constructed the same way and a caller should not have to remember which
// backend happens to be checkable.
func NewStore(_ context.Context, config StoreConfig) (*Store, error) {
	config.applyDefaults()
	if err := config.Validate(); err != nil {
		return nil, err
	}
	embeddingClient, err := embeddingclient.New(config.EmbeddingModel)
	if err != nil {
		return nil, fmt.Errorf("vespa: create embedding client: %w", err)
	}
	return &Store{
		endpoint:         strings.TrimRight(config.Endpoint, "/"),
		schemaName:       config.SchemaName,
		namespace:        config.Namespace,
		fields:           schemaFields{content: config.ContentField, embedding: config.EmbeddingField},
		queryTensorName:  config.QueryTensorName,
		rankingProfile:   config.RankingProfile,
		embeddingClient:  embeddingClient,
		documentBatcher:  config.DocumentBatcher,
		httpClient:       config.HTTPClient,
		maxResponseBytes: cmp.Or(config.MaxResponseBytes, DefaultMaxResponseBytes),
		maxHits:          cmp.Or(config.MaxHits, DefaultMaxHits),
	}, nil
}

// Index embeds documents and writes them through the Vespa Document API.
func (s *Store) Index(ctx context.Context, request *vectorstore.IndexRequest) (err error) {
	if validateErr := request.Validate(); validateErr != nil {
		return fmt.Errorf("vespa.Store.Index: %w", validateErr)
	}
	for index, doc := range request.Documents {
		if doc.Media != nil {
			return fmt.Errorf("vespa.Store.Index: %w: documents[%d] contains unsupported media", vectorstore.ErrInvalidDocument, index)
		}
	}
	for index, doc := range request.Documents {
		for field := range doc.Metadata {
			if s.fields.reserved(field) {
				return fmt.Errorf("vespa: documents[%d] metadata key %q is reserved", index, field)
			}
		}
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
			storedMetadata, paths, err := projectMetadata(doc.Metadata)
			if err != nil {
				return err
			}
			// Filter attributes and presence paths are projections of the same
			// metadata, published atomically with its authoritative JSON value.
			fields := map[string]any{
				namespaceField:     s.namespace,
				metadataField:      storedMetadata,
				metadataPathsField: paths,
				s.fields.content:   doc.Text,
				s.fields.embedding: map[string]any{"values": embedding.Float32Vector(vectors[i])},
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
func (s *Store) Search(ctx context.Context, request *vectorstore.SearchRequest) (response *vectorstore.SearchResponse, err error) {
	var docs []*vectorstore.SearchResult
	if err = request.Validate(); err != nil {
		return nil, fmt.Errorf("vespa.Store.Search: %w", err)
	}
	if err = request.Options.RequireMode(vectorstore.SearchModeSemantic); err != nil {
		return nil, fmt.Errorf("vespa.Store.Search: %w", err)
	}

	defer func() {
		if err == nil {
			err = response.ValidateFor(request)
		}
	}()

	filterFragment, err := s.buildFilter(request.Options.Filter)
	if err != nil {
		return nil, err
	}
	if limit := request.Options.ResultLimit(); limit > s.maxHits {
		return nil, fmt.Errorf("vespa: TopK %d exceeds this application's maxHits of %d, which Vespa applies by trimming the result rather than reporting it",
			limit, s.maxHits)
	}
	vector, err := s.embeddingClient.EmbedText(ctx, request.Query)
	if err != nil {
		return nil, fmt.Errorf("vespa: embed query: %w", err)
	}
	queryVec := embedding.Float32Vector(vector)

	nn := fmt.Sprintf("{targetHits:%d}nearestNeighbor(%s, %s)",
		request.Options.ResultLimit(), s.fields.embedding, s.queryTensorName)
	yql := fmt.Sprintf("select * from %s where %s and %s contains %s", s.schemaName, nn, namespaceField, strconv.Quote(s.namespace))
	if filterFragment != "" {
		yql = yql + " and " + filterFragment
	}

	body := map[string]any{
		"yql":                  yql,
		"hits":                 request.Options.ResultLimit(),
		"presentation.summary": defaultSummary,
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
		if score < request.Options.MinScore {
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

func (s *Store) DeleteWhere(ctx context.Context, predicate filter.Predicate) (err error) {
	if predicate == nil {
		return vectorstore.ErrMissingFilter
	}
	if err = predicate.Validate(); err != nil {
		return fmt.Errorf("vespa.Store.DeleteWhere: %w", err)
	}

	filterFragment, err := s.buildFilter(predicate)
	if err != nil {
		return err
	}
	if filterFragment == "" {
		return errors.New("vespa: refusing to delete on empty filter")
	}

	deleted := make(map[string]struct{})
	for {
		yql := fmt.Sprintf("select %s, %s from %s where (%s) and %s contains %s",
			nativeIDField, metadataField, s.schemaName, filterFragment, namespaceField, strconv.Quote(s.namespace))
		body := map[string]any{
			"yql":                  yql,
			"hits":                 s.maxHits,
			"presentation.summary": defaultSummary,
		}
		hits, err := s.query(ctx, body)
		if err != nil {
			return fmt.Errorf("vespa: enumerate ids: %w", err)
		}
		if len(hits) == 0 {
			return nil
		}
		ids := make([]string, len(hits))
		for index, hit := range hits {
			id, err := s.documentID(hit.ID)
			if err != nil {
				return err
			}
			if _, err := s.readMetadata(id, hit.Fields); err != nil {
				return err
			}
			if _, repeated := deleted[id]; repeated {
				return fmt.Errorf("vespa: delete made no progress: document %q remains visible", id)
			}
			ids[index] = id
		}
		for _, id := range ids {
			path := fmt.Sprintf("/document/v1/%s/%s/docid/%s",
				url.PathEscape(s.namespace), url.PathEscape(s.schemaName), url.PathEscape(id))
			if _, err := s.sendJSON(ctx, http.MethodDelete, path, nil); err != nil {
				return fmt.Errorf("vespa: delete %s: %w", id, err)
			}
			deleted[id] = struct{}{}
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
	if err := jsonv2.Unmarshal(raw, &parsed); err != nil {
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
	v := newVisitor(s.fields)
	if err := expr.Accept(v); err != nil {
		return "", fmt.Errorf("vespa: convert filter: %w", err)
	}
	return v.snapshot(), nil
}

func (s *Store) toDocument(rawID string, fields metadata.Map) (*document.Document, error) {
	id, err := s.documentID(rawID)
	if err != nil {
		return nil, err
	}
	doc := &document.Document{ID: id}
	text, present, err := fields.Decode[string](s.fields.content)
	if err != nil {
		return nil, fmt.Errorf("vespa: decode field %q: %w", s.fields.content, err)
	}
	if !present || text == "" {
		return nil, fmt.Errorf("vespa: document %q is missing string field %q", doc.ID, s.fields.content)
	}
	doc.Text = text
	meta, err := s.readMetadata(id, fields)
	if err != nil {
		return nil, err
	}
	if len(meta) > 0 {
		doc.Metadata = meta
	}
	return doc, nil
}

func (s *Store) readMetadata(id string, fields metadata.Map) (metadata.Map, error) {
	storedMetadata, present, err := fields.Decode[string](metadataField)
	if err != nil {
		return nil, fmt.Errorf("vespa: decode field %q: %w", metadataField, err)
	}
	if !present {
		return nil, fmt.Errorf("vespa: document %q is missing field %q", id, metadataField)
	}
	var meta metadata.Map
	if err := jsonv2.Unmarshal([]byte(storedMetadata), &meta); err != nil {
		return nil, fmt.Errorf("vespa: decode stored metadata: %w", err)
	}
	if meta == nil {
		return nil, fmt.Errorf("vespa: document %q metadata must be an object", id)
	}
	for key := range meta {
		if s.fields.reserved(key) {
			return nil, fmt.Errorf("vespa: stored metadata key %q is reserved", key)
		}
	}
	return meta, nil
}

func (s *Store) sendJSON(ctx context.Context, method, path string, body any) ([]byte, error) {
	u := s.endpoint + path

	var reqBody io.Reader
	if body != nil {
		buf, err := jsonv2.Marshal(body)
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

func (s *Store) documentID(raw string) (string, error) {
	prefix := "id:" + s.namespace + ":" + s.schemaName + "::"
	id, matches := strings.CutPrefix(raw, prefix)
	if !matches || id == "" {
		return "", fmt.Errorf("vespa: search hit %q is outside document scope %q", raw, prefix)
	}
	return id, nil
}
