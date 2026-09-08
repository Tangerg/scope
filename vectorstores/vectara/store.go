package vectara

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

// Exported identifiers keep provider-owned names and defaults out of caller literals.
const (
	Provider = "Vectara"

	// DefaultEndpoint is Vectara's public REST endpoint.
	DefaultEndpoint = "https://api.vectara.io"

	// DefaultAPIVersion targets the v2 API surface.
	DefaultAPIVersion = "v2"

	DefaultMaxResponseBytes = int64(16 * 1024 * 1024)
)

// ErrUnavailableCorpus reports a corpus that exists but is not serving,
// because an administrator disabled it.
var ErrUnavailableCorpus = errors.New("vectara: corpus is unavailable")

// StoreConfig contains configuration options for the Vectara vector
// store. Vectara is a managed RAG service that handles embedding,
// chunking, and retrieval internally — the store sends raw text to
// the API and does NOT need an [embedding.Model]. This is unlike
// every other scope vector store.
type StoreConfig struct {
	// Endpoint is the Vectara API endpoint. Optional: defaults to
	// [DefaultEndpoint].
	Endpoint string

	// APIKey is the Vectara API key. Required.
	APIKey string

	// CorpusKey identifies the Vectara corpus. Required.
	CorpusKey string

	// DocumentBatcher batches documents before upload. Required.
	DocumentBatcher vectorstore.Batcher

	// MetadataPrefix overrides the metadata accessor prefix used by
	// the filter visitor. Optional: defaults to "doc" so filters
	// address `doc.<key>` paths.
	MetadataPrefix string

	// HTTPClient lets callers override transport. Optional:
	// defaults to http.DefaultClient.
	HTTPClient *http.Client

	// MaxResponseBytes bounds every buffered HTTP response. Zero selects
	// [DefaultMaxResponseBytes].
	MaxResponseBytes int64
}

func (s StoreConfig) Validate() error {
	s.applyDefaults()
	if s.APIKey == "" {
		return errors.New("vectara: APIKey is required")
	}
	if s.CorpusKey == "" {
		return errors.New("vectara: CorpusKey is required")
	}
	if lo.IsNil(s.DocumentBatcher) {
		return errors.New("vectara: DocumentBatcher is required")
	}
	if s.MaxResponseBytes < 0 {
		return errors.New("vectara: MaxResponseBytes must not be negative")
	}
	return nil
}

// applyDefaults fills zero fields with documented defaults.
func (s *StoreConfig) applyDefaults() {
	s.Endpoint = cmp.Or(s.Endpoint, DefaultEndpoint)
	if s.MetadataPrefix == "" {
		s.MetadataPrefix = "doc"
	}
	if s.HTTPClient == nil {
		s.HTTPClient = http.DefaultClient
	}
}

var (
	_ vectorstore.Indexer       = (*Store)(nil)
	_ vectorstore.Searcher      = (*Store)(nil)
	_ vectorstore.FilterDeleter = (*Store)(nil)
)

// Store implements vector-store capabilities with Vectara. Vectara handles
// embedding internally, so the store sends document text without generating
// vectors locally.
type Store struct {
	endpoint         string
	apiKey           string
	corpusKey        string
	metadataPrefix   string
	documentBatcher  vectorstore.Batcher
	httpClient       *http.Client
	maxResponseBytes int64
}

// NewStore confirms the corpus is there and serving during construction, which
// is why it takes a context. Vectara owns embedding and scoring, so there is no
// metric to agree on; what a caller can still get wrong is the corpus key or a
// corpus an administrator has disabled, and both belong at wiring rather than
// on the first upload.
func NewStore(ctx context.Context, config StoreConfig) (*Store, error) {
	config.applyDefaults()
	if err := config.Validate(); err != nil {
		return nil, err
	}
	store := &Store{
		endpoint:         strings.TrimRight(config.Endpoint, "/"),
		apiKey:           config.APIKey,
		corpusKey:        config.CorpusKey,
		metadataPrefix:   config.MetadataPrefix,
		documentBatcher:  config.DocumentBatcher,
		httpClient:       config.HTTPClient,
		maxResponseBytes: cmp.Or(config.MaxResponseBytes, DefaultMaxResponseBytes),
	}
	if err := store.verifyCorpus(ctx); err != nil {
		return nil, err
	}
	return store, nil
}

// verifyCorpus reads the corpus this store is bound to.
//
// A disabled corpus is the case worth naming: Vectara's own reason for
// exposing the flag is that an administrator may "respond to a security
// incident by disabling a corpus", so a store that keeps operating against one
// is working against an explicit decision. A wrong key surfaces here too, as
// the read's own error, instead of as a failure on the first document.
func (s *Store) verifyCorpus(ctx context.Context) error {
	path := fmt.Sprintf("/%s/corpora/%s", DefaultAPIVersion, url.PathEscape(s.corpusKey))
	raw, err := s.sendJSON(ctx, http.MethodGet, path, nil)
	if err != nil {
		return fmt.Errorf("vectara: read corpus %s: %w", s.corpusKey, err)
	}
	var corpus struct {
		Key     string `json:"key"`
		Enabled *bool  `json:"enabled"`
	}
	if err = json.Unmarshal(raw, &corpus); err != nil {
		return fmt.Errorf("vectara: decode corpus %s: %w", s.corpusKey, err)
	}
	if corpus.Enabled != nil && !*corpus.Enabled {
		return fmt.Errorf("%w: corpus %s is disabled", ErrUnavailableCorpus, s.corpusKey)
	}
	return nil
}

// Index uploads documents to the corpus via Vectara's index API. The
// service performs its own embedding internally, so no embedding
// client is required here.
func (s *Store) Index(ctx context.Context, request *vectorstore.IndexRequest) (err error) {
	if validateErr := request.Validate(); validateErr != nil {
		return fmt.Errorf("vectara.Store.Index: %w", validateErr)
	}

	var batches []*vectorstore.IndexRequest
	batches, err = request.Batch(ctx, s.documentBatcher)
	if err != nil {
		return fmt.Errorf("vectara: batch documents: %w", err)
	}

	path := fmt.Sprintf("/%s/corpora/%s/documents",
		DefaultAPIVersion, url.PathEscape(s.corpusKey))

	for _, batch := range batches {
		docs := batch.Documents
		for _, doc := range docs {
			id := doc.ID
			metadataValues, err := doc.Metadata.Values()
			if err != nil {
				return fmt.Errorf("vectara: decode metadata for %s: %w", id, err)
			}
			payload := map[string]any{
				"id":       id,
				"type":     "core",
				"metadata": lo.CoalesceMapOrEmpty(metadataValues),
				"document_parts": []any{
					map[string]any{"text": doc.Text},
				},
			}
			if _, err := s.sendJSON(ctx, http.MethodPost, path, payload); err != nil {
				return fmt.Errorf("vectara: upload %s: %w", id, err)
			}
		}
	}
	return nil
}

// Search runs a Vectara semantic search.
func (s *Store) Search(ctx context.Context, req *vectorstore.SearchRequest) (response *vectorstore.SearchResponse, err error) {
	var docs []*vectorstore.SearchResult
	if err = req.Validate(); err != nil {
		return nil, fmt.Errorf("vectara.Store.Search: %w", err)
	}
	if err = req.Options.RequireMode(vectorstore.SearchModeSemantic); err != nil {
		return nil, fmt.Errorf("vectara.Store.Search: %w", err)
	}

	defer func() {
		if err == nil {
			err = response.ValidateFor(req)
		}
	}()

	searchOpts := map[string]any{
		"limit": req.Options.ResultLimit(),
	}
	filterFragment, err := s.buildFilter(req.Options.Filter)
	if err != nil {
		return nil, err
	}
	if filterFragment != "" {
		searchOpts["metadata_filter"] = filterFragment
	}

	payload := map[string]any{
		"query":  req.Query,
		"search": searchOpts,
	}

	path := fmt.Sprintf("/%s/corpora/%s/query",
		DefaultAPIVersion, url.PathEscape(s.corpusKey))
	raw, err := s.sendJSON(ctx, http.MethodPost, path, payload)
	if err != nil {
		return nil, fmt.Errorf("vectara: query: %w", err)
	}

	// Metadata stays raw so a stored integer beyond the exact float64 range
	// reaches the caller unchanged.
	var parsed struct {
		SearchResults []struct {
			Text       string       `json:"text"`
			Score      *float64     `json:"score"`
			DocumentID string       `json:"document_id"`
			Metadata   metadata.Map `json:"document_metadata"`
		} `json:"search_results"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("vectara: decode query response: %w", err)
	}

	docs = make([]*vectorstore.SearchResult, 0, len(parsed.SearchResults))
	for i, hit := range parsed.SearchResults {
		if hit.Score == nil {
			return nil, errors.New("vectara: search result is missing score")
		}
		score, scoreErr := relevanceScore(*hit.Score)
		if scoreErr != nil {
			return nil, scoreErr
		}
		if score < req.Options.MinScore {
			continue
		}
		if hit.DocumentID == "" {
			return nil, fmt.Errorf("vectara: search result %d is missing document_id", i)
		}
		if hit.Text == "" {
			return nil, fmt.Errorf("vectara: search result %d is missing text", i)
		}
		docs = append(docs, &vectorstore.SearchResult{
			Document: &document.Document{ID: hit.DocumentID, Text: hit.Text, Metadata: hit.Metadata},
			Score:    score,
		})
	}
	return &vectorstore.SearchResponse{Results: docs}, nil
}

func (s *Store) DeleteWhere(ctx context.Context, expr filter.Predicate) (err error) {
	if expr == nil {
		return vectorstore.ErrMissingFilter
	}
	if err = expr.Validate(); err != nil {
		return fmt.Errorf("vectara.Store.DeleteWhere: %w", err)
	}

	filterFragment, err := s.buildFilter(expr)
	if err != nil {
		return err
	}
	if filterFragment == "" {
		return errors.New("vectara: refusing to delete on empty filter")
	}

	ids, err := s.matchingDocumentIDs(ctx, filterFragment)
	if err != nil {
		return err
	}
	for _, id := range ids {
		deletePath := fmt.Sprintf("/%s/corpora/%s/documents/%s",
			DefaultAPIVersion, url.PathEscape(s.corpusKey), url.PathEscape(id))
		if _, err := s.sendJSON(ctx, http.MethodDelete, deletePath, nil); err != nil {
			return fmt.Errorf("vectara: delete %s: %w", id, err)
		}
	}
	return nil
}

// listPageSize is the maximum a Vectara list endpoint accepts.
const listPageSize = 100

// matchingDocumentIDs lists every document the filter selects.
//
// Only a missing page key establishes that the listing is complete: a page can
// come back short of the requested limit, or empty, while further pages remain.
// The whole set is collected before DeleteWhere removes anything, because a
// page key belongs to the listing that produced it — walking and deleting
// together would resume paging through a document set that no longer exists.
func (s *Store) matchingDocumentIDs(ctx context.Context, filterFragment string) ([]string, error) {
	listPath := fmt.Sprintf("/%s/corpora/%s/documents?metadata_filter=%s&limit=%d",
		DefaultAPIVersion, url.PathEscape(s.corpusKey), url.QueryEscape(filterFragment), listPageSize)

	var ids []string
	for {
		raw, err := s.sendJSON(ctx, http.MethodGet, listPath, nil)
		if err != nil {
			return nil, fmt.Errorf("vectara: list documents: %w", err)
		}
		var parsed struct {
			Documents []struct {
				ID string `json:"id"`
			} `json:"documents"`
			Metadata struct {
				PageKey string `json:"page_key"`
			} `json:"metadata"`
		}
		if err := json.Unmarshal(raw, &parsed); err != nil {
			return nil, fmt.Errorf("vectara: decode list response: %w", err)
		}
		for index, doc := range parsed.Documents {
			if doc.ID == "" {
				return nil, fmt.Errorf("vectara: listed document %d has no id", index)
			}
			ids = append(ids, doc.ID)
		}
		if parsed.Metadata.PageKey == "" {
			return ids, nil
		}
		listPath = fmt.Sprintf("/%s/corpora/%s/documents?metadata_filter=%s&limit=%d&page_key=%s",
			DefaultAPIVersion, url.PathEscape(s.corpusKey), url.QueryEscape(filterFragment),
			listPageSize, url.QueryEscape(parsed.Metadata.PageKey))
	}
}

// vectaraMinimumScore and vectaraMaximumScore are the scale Vectara documents
// for the query this store sends: "results from Vectara are scored on a scale
// from -1 to 1, with 1 being a perfect match and -1 having absolutely nothing
// to do with the query".
const (
	vectaraMinimumScore = -1.0
	vectaraMaximumScore = 1.0
)

// relevanceScore maps a Vectara relevance score onto Core's range.
//
// The score is not already a Core score, which is what passing it through
// ScoreFromValue assumed. On Vectara's documented -1 to 1 scale a result
// scoring 0.5 is three quarters of the way to a perfect match, and reporting
// 0.5 understated it; every negative score, which Vectara defines as having
// nothing to do with the query, collapsed onto 0 and lost its order. Mapping a
// [-1, 1] similarity into [0, 1] is what ScoreFromCosineSimilarity does — the
// arithmetic is the bounded-similarity mapping, not a claim that Vectara
// returns a cosine.
//
// A score outside that scale is reported rather than rescaled. Vectara
// documents reranked scores as running from negative to positive infinity,
// typically between about -10 and 10, and a reranker is corpus configuration
// this store does not set — so an out-of-range score means the scale this
// store maps no longer applies, and squeezing it onto the bound would hide
// that behind a plausible number.
func relevanceScore(raw float64) (vectorstore.Score, error) {
	if math.IsNaN(raw) || raw < vectaraMinimumScore || raw > vectaraMaximumScore {
		return 0, fmt.Errorf(
			"vectara: relevance score %v is outside the documented [%v, %v] scale; a corpus reranker reports an unbounded score this store cannot map",
			raw, vectaraMinimumScore, vectaraMaximumScore)
	}
	return vectorstore.ScoreFromCosineSimilarity(raw), nil
}

func (s *Store) buildFilter(expr filter.Predicate) (string, error) {
	if expr == nil {
		return "", nil
	}
	v := newVisitor(s.metadataPrefix)
	if err := expr.Accept(v); err != nil {
		return "", fmt.Errorf("vectara: convert filter: %w", err)
	}
	return v.snapshot(), nil
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
	req.Header.Set("x-api-key", s.apiKey)
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
