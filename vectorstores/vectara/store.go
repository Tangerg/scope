package vectara

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
	"strings"
	"unicode/utf8"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

var (
	_ vectorstore.Indexer   = (*Store)(nil)
	_ vectorstore.Searcher  = (*Store)(nil)
	_ vectorstore.IDDeleter = (*Store)(nil)
)

type Store struct {
	endpoint         string
	apiKey           string
	corpusKey        string
	documentBatcher  vectorstore.Batcher
	httpClient       *http.Client
	maxResponseBytes int64
}

func NewStore(ctx context.Context, config StoreConfig) (*Store, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	client := config.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	store := &Store{endpoint: strings.TrimRight(cmp.Or(config.Endpoint, DefaultEndpoint), "/"), apiKey: config.APIKey, corpusKey: config.CorpusKey, documentBatcher: config.DocumentBatcher, httpClient: client, maxResponseBytes: cmp.Or(config.MaxResponseBytes, DefaultMaxResponseBytes)}
	if _, err := store.selectDocuments(ctx, nil); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *Store) documentsPath() string {
	return "/" + apiVersion + "/corpora/" + url.PathEscape(s.corpusKey) + "/documents"
}

func (s *Store) documentPath(id string) string {
	segment := url.PathEscape(id)
	if segment == "." || segment == ".." {
		segment = strings.ReplaceAll(segment, ".", "%2E")
	}
	return s.documentsPath() + "/" + segment
}

func (s *Store) Index(ctx context.Context, request *vectorstore.IndexRequest) error {
	if err := request.Validate(); err != nil {
		return err
	}
	for _, doc := range request.Documents {
		if doc.Media != nil {
			return vectorstore.ErrInvalidDocument
		}
	}
	records := make(map[string]nativeIndexDocument, len(request.Documents))
	for _, doc := range request.Documents {
		record, err := encodeDocument(doc)
		if err != nil {
			return err
		}
		records[doc.ID] = record
	}
	batches, err := request.Batch(ctx, s.documentBatcher)
	if err != nil {
		return err
	}
	if _, err = s.selectDocuments(ctx, nil); err != nil {
		return err
	}
	for _, batch := range batches {
		for _, doc := range batch.Documents {
			if err = s.DeleteIDs(ctx, []string{doc.ID}); err != nil {
				return err
			}
			raw, writeErr := s.sendJSON(ctx, http.MethodPost, s.documentsPath()+"?wait_for=searchable", records[doc.ID], http.StatusCreated)
			if writeErr != nil {
				return writeErr
			}
			var acknowledgment nativeDocument
			if err = jsonv2.Unmarshal(raw, &acknowledgment); err != nil {
				return err
			}
			if acknowledgment.ID != doc.ID {
				return errors.New("vectara: native creation did not acknowledge the requested identity")
			}
		}
	}
	return nil
}

func (s *Store) selectDocuments(ctx context.Context, predicate filter.Predicate) ([]*document.Document, error) {
	if err := s.readCorpus(ctx); err != nil {
		return nil, err
	}
	var selected []*document.Document
	seenIDs, seenPages := make(map[string]struct{}), make(map[string]struct{})
	pageKey := ""
	for {
		if err := context.Cause(ctx); err != nil {
			return nil, err
		}
		query := url.Values{"limit": {fmt.Sprint(listPageSize)}}
		if pageKey != "" {
			query.Set("page_key", pageKey)
		}
		raw, err := s.sendJSON(ctx, http.MethodGet, s.documentsPath()+"?"+query.Encode(), nil, http.StatusOK)
		if err != nil {
			return nil, err
		}
		var page struct {
			Documents *[]struct {
				ID string `json:"id"`
			} `json:"documents"`
			Metadata struct {
				PageKey string `json:"page_key"`
			} `json:"metadata"`
		}
		if err = jsonv2.Unmarshal(raw, &page); err != nil {
			return nil, err
		}
		if page.Documents == nil || len(*page.Documents) > listPageSize {
			return nil, errors.New("vectara: invalid native document page")
		}
		for _, listed := range *page.Documents {
			if strings.TrimSpace(listed.ID) == "" || !utf8.ValidString(listed.ID) {
				return nil, vectorstore.ErrMissingDocumentID
			}
			if _, duplicate := seenIDs[listed.ID]; duplicate {
				return nil, errors.New("vectara: native listing repeated an identity")
			}
			seenIDs[listed.ID] = struct{}{}
			raw, err = s.sendJSON(ctx, http.MethodGet, s.documentPath(listed.ID), nil, http.StatusOK)
			if err != nil {
				return nil, err
			}
			var record nativeDocument
			if err = jsonv2.Unmarshal(raw, &record); err != nil {
				return nil, err
			}
			if record.ID != listed.ID || len(record.Parts) != 1 || len(record.Tables) != 0 || len(record.Images) != 0 {
				return nil, errors.New("vectara: native document has an invalid identity or content shape")
			}
			if record.Parts[0].Context != "" || record.Parts[0].TableID != "" || record.Parts[0].ImageID != "" || len(record.Parts[0].CustomDimensions) != 0 {
				return nil, errors.New("vectara: native part has unsupported context or media")
			}
			doc, decodeErr := decodeDocument(record.ID, record.Parts[0].Text, record.Metadata)
			if decodeErr != nil {
				return nil, decodeErr
			}
			matched := true
			if predicate != nil {
				values, valuesErr := doc.Metadata.Values()
				if valuesErr != nil {
					return nil, valuesErr
				}
				matched, err = filter.Match(predicate, values)
				if err != nil {
					return nil, err
				}
			}
			if matched {
				selected = append(selected, doc)
			}
		}
		pageKey = page.Metadata.PageKey
		if pageKey == "" {
			return selected, nil
		}
		if _, repeated := seenPages[pageKey]; repeated {
			return nil, errors.New("vectara: native page key repeated")
		}
		seenPages[pageKey] = struct{}{}
	}
}

func (s *Store) readCorpus(ctx context.Context) error {
	raw, err := s.sendJSON(ctx, http.MethodGet, "/"+apiVersion+"/corpora/"+url.PathEscape(s.corpusKey), nil, http.StatusOK)
	if err != nil {
		return err
	}
	var corpus struct {
		Key              string `json:"key"`
		Enabled          *bool  `json:"enabled"`
		ChatHistory      bool   `json:"chat_history_corpus"`
		CustomDimensions []any  `json:"custom_dimensions"`
	}
	if err = jsonv2.Unmarshal(raw, &corpus); err != nil {
		return err
	}
	if corpus.Key != s.corpusKey || corpus.Enabled == nil || !*corpus.Enabled || corpus.ChatHistory || len(corpus.CustomDimensions) != 0 {
		return errors.New("vectara: native corpus must be enabled, store documents, and have no custom dimensions")
	}
	return nil
}

func (s *Store) Search(ctx context.Context, request *vectorstore.SearchRequest) (response *vectorstore.SearchResponse, err error) {
	if err = request.Validate(); err != nil {
		return nil, err
	}
	if err = request.Options.RequireMode(vectorstore.SearchModeSemantic); err != nil {
		return nil, err
	}
	defer func() {
		if err == nil {
			err = response.ValidateFor(request)
		}
		if err != nil {
			response = nil
		}
	}()
	selected, err := s.selectDocuments(ctx, request.Options.Filter)
	if err != nil {
		return nil, err
	}
	if len(selected) == 0 {
		return &vectorstore.SearchResponse{}, nil
	}
	groups := []identitySelection{{}}
	if request.Options.Filter != nil {
		groups, err = selectIdentities(selected)
		if err != nil {
			return nil, err
		}
	}
	var ranked []rankedDocument
	seen := make(map[string]struct{})
	for _, group := range groups {
		payload := struct {
			Query  string `json:"query"`
			Search struct {
				Limit                int     `json:"limit"`
				MetadataFilter       string  `json:"metadata_filter,omitempty"`
				LexicalInterpolation float64 `json:"lexical_interpolation"`
				Reranker             struct {
					Type string `json:"type"`
				} `json:"reranker"`
			} `json:"search"`
			Generation struct {
				Enabled bool `json:"enabled"`
			} `json:"generation"`
			StreamResponse            bool `json:"stream_response"`
			SaveHistory               bool `json:"save_history"`
			IntelligentQueryRewriting bool `json:"intelligent_query_rewriting"`
		}{Query: request.Query}
		payload.Search.Limit = request.Options.ResultLimit()
		payload.Search.MetadataFilter = group.expression
		payload.Search.Reranker.Type = nativeRerankerNone
		raw, queryErr := s.sendJSON(ctx, http.MethodPost, "/"+apiVersion+"/corpora/"+url.PathEscape(s.corpusKey)+"/query", payload, http.StatusOK)
		if queryErr != nil {
			return nil, queryErr
		}
		var parsed struct {
			SearchResults *[]struct {
				ResultType string            `json:"result_type"`
				Text       string            `json:"text"`
				Score      *float64          `json:"score"`
				ID         string            `json:"document_id"`
				Metadata   map[string]string `json:"document_metadata"`
				CorpusKey  string            `json:"corpus_key"`
			} `json:"search_results"`
		}
		if err = jsonv2.Unmarshal(raw, &parsed); err != nil {
			return nil, err
		}
		if parsed.SearchResults == nil || len(*parsed.SearchResults) > request.Options.ResultLimit() {
			return nil, errors.New("vectara: invalid native result array or limit")
		}
		for _, hit := range *parsed.SearchResults {
			if hit.ResultType != nativeTextResultType || hit.Score == nil || hit.CorpusKey != "" && hit.CorpusKey != s.corpusKey {
				return nil, errors.New("vectara: native hit has an invalid result type, score or corpus")
			}
			doc, decodeErr := decodeDocument(hit.ID, hit.Text, hit.Metadata)
			if decodeErr != nil {
				return nil, decodeErr
			}
			if _, duplicate := seen[doc.ID]; duplicate {
				return nil, errors.New("vectara: native ranking repeated an identity")
			}
			seen[doc.ID] = struct{}{}
			if group.ids != nil {
				if !slices.Contains(group.ids, doc.ID) {
					return nil, errors.New("vectara: native hit is outside selected identities")
				}
				values, valuesErr := doc.Metadata.Values()
				if valuesErr != nil {
					return nil, valuesErr
				}
				matched, matchErr := filter.Match(request.Options.Filter, values)
				if matchErr != nil {
					return nil, matchErr
				}
				if !matched {
					return nil, errors.New("vectara: native hit changed Core membership")
				}
			}
			score, scoreErr := relevanceScore(*hit.Score)
			if scoreErr != nil {
				return nil, scoreErr
			}
			result, resultErr := vectorstore.NewSearchResult(doc, score)
			if resultErr != nil {
				return nil, resultErr
			}
			ranked = append(ranked, rankedDocument{result: result, rank: *hit.Score})
		}
	}
	slices.SortFunc(ranked, func(left, right rankedDocument) int {
		if order := cmp.Compare(right.rank, left.rank); order != 0 {
			return order
		}
		return strings.Compare(left.result.Document.ID, right.result.Document.ID)
	})
	ranked = ranked[:min(len(ranked), request.Options.ResultLimit())]
	response = &vectorstore.SearchResponse{}
	for _, hit := range ranked {
		if hit.result.Score >= request.Options.MinScore {
			response.Results = append(response.Results, hit.result)
		}
	}
	return response, nil
}

func (s *Store) DeleteIDs(ctx context.Context, ids []string) error {
	for _, id := range ids {
		if strings.TrimSpace(id) == "" || !utf8.ValidString(id) {
			return vectorstore.ErrMissingDocumentID
		}
	}
	seen := make(map[string]struct{})
	for _, id := range ids {
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		if _, err := s.sendJSON(ctx, http.MethodDelete, s.documentPath(id), nil, http.StatusNoContent, http.StatusNotFound); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) sendJSON(ctx context.Context, method, path string, body any, expected ...int) ([]byte, error) {
	var reader io.Reader
	if body != nil {
		encoded, err := jsonv2.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, s.endpoint+path, reader)
	if err != nil {
		return nil, err
	}
	request.Header.Set("x-api-key", s.apiKey)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	response, err := s.httpClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, s.maxResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > s.maxResponseBytes {
		return nil, fmt.Errorf("vectara: response exceeds %d-byte limit", s.maxResponseBytes)
	}
	if !slices.Contains(expected, response.StatusCode) {
		return nil, fmt.Errorf("vectara: unexpected native status %d: %s", response.StatusCode, raw)
	}
	return raw, nil
}
