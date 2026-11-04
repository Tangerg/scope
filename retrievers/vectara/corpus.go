package vectara

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/httpclient"
	"github.com/Tangerg/scope/rag"
)

var _ rag.Retriever = (*Corpus)(nil)

// Corpus binds one native corpus to a host-owned authorization scope.
// Queries cannot alter the corpus, allowed identities or response limits.
type Corpus struct {
	endpoint         string
	apiKey           string
	corpusKey        string
	scope            *boundScope
	limit            int
	httpClient       *http.Client
	maxResponseBytes int64
}

func New(ctx context.Context, config Config) (*Corpus, error) {
	if err := config.validateConnection(); err != nil {
		return nil, err
	}
	scope, err := bindScope(config.AllowedDocumentIDs)
	if err != nil {
		return nil, err
	}
	client, err := httpclient.New(ctx, httpclient.Config{RejectRedirects: true, HTTPClient: config.HTTPClient, BaseURL: cmp.Or(config.Endpoint, DefaultEndpoint)})
	if err != nil {
		return nil, err
	}
	store := &Corpus{endpoint: strings.TrimRight(cmp.Or(config.Endpoint, DefaultEndpoint), "/"), apiKey: config.APIKey, corpusKey: config.CorpusKey, scope: scope, limit: cmp.Or(config.Limit, 5), httpClient: client, maxResponseBytes: cmp.Or(config.MaxResponseBytes, DefaultMaxResponseBytes)}
	if err := store.readCorpus(ctx); err != nil {
		return nil, err
	}
	return store, nil
}

func (c *Corpus) documentsPath() string {
	return "/" + apiVersion + "/corpora/" + url.PathEscape(c.corpusKey) + "/documents"
}

func (c *Corpus) documentPath(id string) string {
	segment := url.PathEscape(id)
	if segment == "." || segment == ".." {
		segment = strings.ReplaceAll(segment, ".", "%2E")
	}
	return c.documentsPath() + "/" + segment
}

// ErrDocumentExists means the service rejected creation of an existing ID.
var ErrDocumentExists = errors.New("vectara: document already exists")

// CreateDocument creates one independently acknowledged text document and waits
// until it is searchable. It never deletes or replaces an existing document.
// A transport or malformed acknowledgment error can leave the write outcome unknown.
func (c *Corpus) CreateDocument(ctx context.Context, doc *document.Document) error {
	if err := doc.Validate(); err != nil {
		return err
	}
	if doc.Media != nil {
		return fmt.Errorf("vectara: text ingestion does not accept media: %w", errors.ErrUnsupported)
	}
	if err := sourceMetadata(doc.Metadata); err != nil {
		return err
	}
	if err := c.scope.authorize(doc.ID); err != nil {
		return err
	}
	var stored jsontext.Value
	if doc.Metadata != nil || doc.Provenance != nil {
		raw, encodeErr := doc.MarshalStoredMetadata()
		if encodeErr != nil {
			return encodeErr
		}
		stored = raw
	}
	payload := struct {
		ID       string              `json:"id"`
		Type     string              `json:"type"`
		Metadata jsontext.Value      `json:"metadata,omitzero"`
		Parts    []map[string]string `json:"document_parts"`
	}{doc.ID, nativeDocumentType, stored, []map[string]string{{"text": doc.Text}}}
	raw, err := c.sendJSON(ctx, http.MethodPost, c.documentsPath()+"?wait_for=searchable", payload, http.StatusCreated)
	if err != nil {
		return err
	}
	var ack struct {
		ID string `json:"id"`
	}
	if err = jsonv2.Unmarshal(raw, &ack); err != nil {
		return err
	}
	if ack.ID != doc.ID {
		return errors.New("vectara: creation acknowledgment identity mismatch")
	}
	return nil
}

// DeleteDocument explicitly removes one authorized source identity. A missing
// document is already deleted.
func (c *Corpus) DeleteDocument(ctx context.Context, id string) error {
	if err := c.scope.authorize(id); err != nil {
		return err
	}
	_, err := c.sendJSON(ctx, http.MethodDelete, c.documentPath(id), nil, http.StatusNoContent, http.StatusNotFound)
	return err
}

func (c *Corpus) readCorpus(ctx context.Context) error {
	raw, err := c.sendJSON(ctx, http.MethodGet, "/"+apiVersion+"/corpora/"+url.PathEscape(c.corpusKey), nil, http.StatusOK)
	if err != nil {
		return err
	}
	var corpus struct {
		Key         string `json:"key"`
		Enabled     *bool  `json:"enabled"`
		ChatHistory bool   `json:"chat_history_corpus"`
	}
	if err = jsonv2.Unmarshal(raw, &corpus); err != nil {
		return err
	}
	if corpus.Key != c.corpusKey || corpus.Enabled == nil || !*corpus.Enabled || corpus.ChatHistory {
		return errors.New("vectara: native corpus must be an enabled document corpus")
	}
	return nil
}

// Retrieve returns the service's passage order and native query-relative scores.
func (c *Corpus) Retrieve(ctx context.Context, q rag.Query) (rag.Candidates, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := q.Validate(); err != nil {
		return nil, err
	}
	search := querySearch{
		Limit: c.limit, LexicalInterpolation: semanticLexicalInterpolation,
		Reranker: queryReranker{Type: nativeRerankerNone},
	}
	if c.scope != nil {
		if len(c.scope.ids) == 0 {
			return rag.Candidates{}, nil
		}
		search.MetadataFilter = c.scope.expression
	}
	raw, err := c.sendJSON(ctx, http.MethodPost, "/"+apiVersion+"/corpora/"+url.PathEscape(c.corpusKey)+"/query",
		queryRequest{Query: q.Text(), Search: search}, http.StatusOK)
	if err != nil {
		return nil, err
	}
	var res struct {
		Hits []passage `json:"search_results"`
	}
	if err = jsonv2.Unmarshal(raw, &res); err != nil {
		return nil, err
	}
	if res.Hits == nil || len(res.Hits) > c.limit {
		return nil, errors.New("vectara: missing or excessive search results")
	}
	hits := make(rag.Candidates, 0, len(res.Hits))
	for _, hit := range res.Hits {
		if err = c.scope.authorize(hit.DocumentID); err != nil {
			return nil, err
		}
		candidate, err := hit.candidate(c.corpusKey)
		if err != nil {
			return nil, err
		}
		hits = append(hits, candidate)
	}
	return hits, nil
}

func (c *Corpus) sendJSON(ctx context.Context, method, path string, body any, expected ...int) ([]byte, error) {
	var reader io.Reader
	if body != nil {
		encoded, err := jsonv2.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.endpoint+path, reader)
	if err != nil {
		return nil, err
	}
	request.Header.Set("x-api-key", c.apiKey)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, c.maxResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > c.maxResponseBytes {
		return nil, fmt.Errorf("vectara: response exceeds %d-byte limit", c.maxResponseBytes)
	}
	if response.StatusCode == http.StatusConflict && method == http.MethodPost && strings.HasPrefix(path, c.documentsPath()+"?") {
		return nil, ErrDocumentExists
	}
	if !slices.Contains(expected, response.StatusCode) {
		return nil, fmt.Errorf("vectara: unexpected native status %d: %s", response.StatusCode, raw)
	}
	return raw, nil
}
