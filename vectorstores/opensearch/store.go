package opensearch

import (
	"bytes"
	"cmp"
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/Tangerg/scope/core/embeddingclient"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

var (
	_ vectorstore.Indexer       = (*Store)(nil)
	_ vectorstore.Searcher      = (*Store)(nil)
	_ vectorstore.IDDeleter     = (*Store)(nil)
	_ vectorstore.FilterDeleter = (*Store)(nil)
)

type Store struct {
	client           APIClient
	indexName        string
	schema           nativeSchema
	embeddingClient  embeddingclient.Client
	documentBatcher  vectorstore.Batcher
	maxResponseBytes int64
}

func NewStore(ctx context.Context, config StoreConfig) (*Store, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	model, err := embeddingclient.New(config.EmbeddingModel)
	if err != nil {
		return nil, err
	}
	store := &Store{client: config.Client, indexName: cmp.Or(config.IndexName, DefaultIndexName), embeddingClient: model, documentBatcher: config.DocumentBatcher, maxResponseBytes: cmp.Or(config.MaxResponseBytes, DefaultMaxResponseBytes)}
	raw, err := store.request(ctx, http.MethodGet, "/"+store.indexName+"/_mapping", nil, nil)
	if err != nil {
		if failure, ok := errors.AsType[*httpFailure](err); ok && failure.status == http.StatusNotFound {
			return nil, fmt.Errorf("%w: %w", ErrIndexMissing, err)
		}
		return nil, err
	}
	var mappings map[string]mappedIndex
	if err = jsonv2.Unmarshal(raw, &mappings); err != nil {
		return nil, err
	}
	mapping, exists := mappings[store.indexName]
	if !exists || len(mappings) != 1 {
		return nil, fmt.Errorf("%w: requires one concrete index without aliases", ErrIncompatibleIndex)
	}
	raw, err = store.request(ctx, http.MethodGet, "/"+store.indexName+"/_settings", url.Values{"flat_settings": {"true"}, "include_defaults": {"true"}, "name": {"index.knn,index.derived_source.enabled,index.knn.derived_source.enabled,index.default_pipeline,index.final_pipeline,index.search.default_pipeline,index.max_result_window"}}, nil)
	if err != nil {
		return nil, err
	}
	var settings map[string]nativeSettings
	if err = jsonv2.Unmarshal(raw, &settings); err != nil {
		return nil, err
	}
	native, exists := settings[store.indexName]
	if !exists || len(settings) != 1 {
		return nil, fmt.Errorf("%w: settings do not describe the concrete index", ErrIncompatibleIndex)
	}
	store.schema, err = mapping.Mappings.schema(native)
	if err != nil {
		return nil, err
	}
	if _, err = store.selectMatches(ctx, nil); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *Store) request(ctx context.Context, method, path string, query url.Values, body []byte) (raw []byte, err error) {
	endpoint := (&url.URL{Path: path, RawQuery: query.Encode()}).String()
	request, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	contentType := "application/json"
	if path == "/_bulk" {
		contentType = "application/x-ndjson"
	}
	request.Header.Set("Content-Type", contentType)
	response, err := s.client.Stream(request)
	if err != nil {
		if response != nil && response.Body != nil {
			err = errors.Join(err, response.Body.Close())
		}
		return nil, err
	}
	if response == nil || response.Body == nil {
		return nil, errors.New("opensearch: native client returned no response body")
	}
	defer func() { err = errors.Join(err, response.Body.Close()) }()
	raw, err = io.ReadAll(io.LimitReader(response.Body, s.maxResponseBytes+1))
	if err != nil {
		return raw, err
	}
	if int64(len(raw)) > s.maxResponseBytes {
		return nil, errors.New("opensearch: native response exceeds MaxResponseBytes")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return raw, &httpFailure{status: response.StatusCode, body: string(raw)}
	}
	return raw, nil
}

func (s *Store) Index(ctx context.Context, request *vectorstore.IndexRequest) error {
	if err := request.Validate(); err != nil {
		return err
	}
	records := make(map[string]storedDocument, len(request.Documents))
	for _, doc := range request.Documents {
		if doc.Media != nil {
			return vectorstore.ErrInvalidDocument
		}
		if len(doc.ID) > nativeMaximumIDBytes {
			return errors.New("opensearch: document ID exceeds native 512-byte limit")
		}
		facts, err := doc.Metadata.MarshalJSON()
		if err != nil {
			return err
		}
		records[doc.ID] = storedDocument{Content: doc.Text, MetadataJSON: string(facts)}
	}
	batches, err := request.Batch(ctx, s.documentBatcher)
	if err != nil {
		return err
	}
	var prepared []bulkPublication
	for _, batch := range batches {
		texts, err := batch.Texts()
		if err != nil {
			return err
		}
		vectors, err := s.embeddingClient.EmbedTexts(ctx, texts)
		if err != nil {
			return err
		}
		var publication bulkPublication
		for i, doc := range batch.Documents {
			vector, err := s.schema.indexVector(vectors[i])
			if err != nil {
				return err
			}
			record := records[doc.ID]
			record.Embedding = vector
			action := bulkAction{Index: &bulkActionTarget{Index: s.indexName, ID: doc.ID}}
			publication.body, err = appendBulkLine(publication.body, action)
			if err != nil {
				return err
			}
			publication.body, err = appendBulkLine(publication.body, record)
			if err != nil {
				return err
			}
			publication.ids = append(publication.ids, doc.ID)
		}
		prepared = append(prepared, publication)
	}
	for _, publication := range prepared {
		if err := s.publish(ctx, publication, bulkOperationIndex, false); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) publish(ctx context.Context, publication bulkPublication, operation bulkOperation, allowMissing bool) error {
	raw, err := s.request(ctx, http.MethodPost, "/_bulk", nil, publication.body)
	if err != nil {
		return err
	}
	var response bulkResponse
	if err := jsonv2.Unmarshal(raw, &response); err != nil {
		return err
	}
	return response.validate(operation, s.indexName, publication.ids, allowMissing)
}

func (s *Store) Search(ctx context.Context, request *vectorstore.SearchRequest) (response *vectorstore.SearchResponse, err error) {
	if err = request.Validate(); err != nil {
		return nil, err
	}
	if err = request.Options.RequireMode(vectorstore.SearchModeSemantic); err != nil {
		return nil, err
	}
	if request.Options.Filter != nil && s.schema.engine == "nmslib" {
		return nil, fmt.Errorf("%w: opensearch: filtered KNN requires Lucene or Faiss", errors.ErrUnsupported)
	}
	if request.Options.ResultLimit() > s.schema.resultWindow {
		return nil, fmt.Errorf("%w: TopK exceeds native result window %d", vectorstore.ErrInvalidOptions, s.schema.resultWindow)
	}
	defer func() {
		if err == nil {
			err = response.ValidateFor(request)
		}
		if err != nil {
			response = nil
		}
	}()
	matches, err := s.selectMatches(ctx, request.Options.Filter)
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return &vectorstore.SearchResponse{}, nil
	}
	vector, err := s.embeddingClient.EmbedText(ctx, request.Query)
	if err != nil {
		return nil, err
	}
	query, err := s.schema.vector(vector)
	if err != nil {
		return nil, err
	}
	groups := [][]string{nil}
	if request.Options.Filter != nil {
		groups = slices.Collect(slices.Chunk(slices.Sorted(maps.Keys(matches)), filterBatchSize))
	}
	var ranked []scoredDocument
	seen := make(map[string]struct{})
	for _, ids := range groups {
		knn := &nearestNeighborQuery{Vector: query, K: request.Options.ResultLimit()}
		if ids != nil {
			knn.Filter = &idsQueryClause{IDs: idsQuery{Values: ids}}
		}
		body, err := jsonv2.Marshal(searchRequest{Size: request.Options.ResultLimit(), Query: &knnQuery{KNN: map[string]*nearestNeighborQuery{embeddingField: knn}}, Source: true, TrackTotalHits: true, SeqNoPrimaryTerm: true, StoredFields: []string{"_routing"}})
		if err != nil {
			return nil, err
		}
		raw, err := s.request(ctx, http.MethodPost, "/"+s.indexName+"/_search", url.Values{"allow_partial_search_results": {"false"}}, body)
		if err != nil {
			return nil, err
		}
		var page searchResponse
		if err := jsonv2.Unmarshal(raw, &page); err != nil {
			return nil, err
		}
		if err := page.validate(s.indexName); err != nil {
			return nil, err
		}
		hits := *page.Hits.Hits
		if int64(len(hits)) != min(*page.Hits.Total.Value, int64(request.Options.ResultLimit())) {
			return nil, errors.New("opensearch: native search did not acknowledge every hit")
		}
		for _, hit := range hits {
			if _, duplicate := seen[hit.ID]; duplicate {
				return nil, errors.New("opensearch: native search repeated a document ID")
			}
			seen[hit.ID] = struct{}{}
			doc, err := s.schema.decode(hit)
			if err != nil {
				return nil, err
			}
			if request.Options.Filter != nil {
				if !slices.Contains(ids, hit.ID) {
					return nil, errors.New("opensearch: native hit is outside Core membership")
				}
				values, decodeErr := doc.Metadata.Values()
				if decodeErr != nil {
					return nil, decodeErr
				}
				matched, matchErr := filter.Match(request.Options.Filter, values)
				if matchErr != nil {
					return nil, matchErr
				}
				if !matched {
					return nil, errors.New("opensearch: native hit changed Core membership")
				}
			}
			if hit.Score == nil {
				return nil, errors.New("opensearch: native hit is missing its score")
			}
			score, err := s.schema.score(*hit.Score)
			if err != nil {
				return nil, err
			}
			result, err := vectorstore.NewSearchResult(doc, score)
			if err != nil {
				return nil, err
			}
			ranked = append(ranked, scoredDocument{result: result, rank: *hit.Score})
		}
	}
	slices.SortFunc(ranked, func(left, right scoredDocument) int {
		if order := cmp.Compare(right.rank, left.rank); order != 0 {
			return order
		}
		return strings.Compare(left.result.Document.ID, right.result.Document.ID)
	})
	ranked = ranked[:min(len(ranked), request.Options.ResultLimit())]
	var results []*vectorstore.SearchResult
	for _, hit := range ranked {
		if hit.result.Score >= request.Options.MinScore {
			results = append(results, hit.result)
		}
	}
	return &vectorstore.SearchResponse{Results: results}, nil
}

func (s *Store) selectMatches(ctx context.Context, predicate filter.Predicate) (selected map[string]searchHit, err error) {
	body, err := jsonv2.Marshal(searchRequest{Size: filterBatchSize, Source: true, TrackTotalHits: true, SeqNoPrimaryTerm: true, StoredFields: []string{"_routing"}, Sort: []string{"_doc"}})
	if err != nil {
		return nil, err
	}
	raw, callErr := s.request(ctx, http.MethodPost, "/"+s.indexName+"/_search", url.Values{"scroll": {"1m"}, "allow_partial_search_results": {"false"}}, body)
	scrollID := ""
	defer func() {
		if scrollID == "" {
			return
		}
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		payload, encodeErr := jsonv2.Marshal(struct {
			IDs []string `json:"scroll_id"`
		}{IDs: []string{scrollID}})
		if encodeErr != nil {
			err = errors.Join(err, encodeErr)
			selected = nil
			return
		}
		cleanupRaw, cleanupErr := s.request(cleanup, http.MethodDelete, "/_search/scroll", nil, payload)
		if cleanupErr == nil {
			var outcome struct {
				Succeeded *bool `json:"succeeded"`
				Freed     *int  `json:"num_freed"`
			}
			cleanupErr = jsonv2.Unmarshal(cleanupRaw, &outcome)
			if cleanupErr == nil && (outcome.Succeeded == nil || !*outcome.Succeeded || outcome.Freed == nil || *outcome.Freed < 0) {
				cleanupErr = errors.New("opensearch: native scroll cleanup was not acknowledged")
			}
		}
		err = errors.Join(err, cleanupErr)
		if err != nil {
			selected = nil
		}
	}()
	selected = make(map[string]searchHit)
	seen := make(map[string]struct{})
	var total int64 = -1
	for {
		var page searchResponse
		decodeErr := jsonv2.Unmarshal(raw, &page)
		if page.ScrollID != "" {
			scrollID = page.ScrollID
		}
		if err = errors.Join(callErr, decodeErr); err != nil {
			return nil, err
		}
		if err = page.validate(s.indexName); err != nil {
			return nil, err
		}
		if total < 0 {
			total = *page.Hits.Total.Value
		}
		if total != *page.Hits.Total.Value || page.ScrollID == "" {
			return nil, errors.New("opensearch: native scroll lost its total or cursor")
		}
		hits := *page.Hits.Hits
		if len(hits) == 0 {
			if int64(len(seen)) != total {
				return nil, errors.New("opensearch: native scroll ended before every document")
			}
			return selected, nil
		}
		for _, hit := range hits {
			if _, duplicate := seen[hit.ID]; duplicate {
				return nil, errors.New("opensearch: native scroll repeated a document ID")
			}
			seen[hit.ID] = struct{}{}
			doc, decodeErr := s.schema.decode(hit)
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
				hit.Source = nil
				selected[hit.ID] = hit
			}
		}
		if int64(len(seen)) > total {
			return nil, errors.New("opensearch: native scroll exceeded its declared total")
		}
		payload, encodeErr := jsonv2.Marshal(struct {
			ID       string `json:"scroll_id"`
			Lifetime string `json:"scroll"`
		}{ID: scrollID, Lifetime: "1m"})
		if encodeErr != nil {
			return nil, encodeErr
		}
		raw, callErr = s.request(ctx, http.MethodPost, "/_search/scroll", nil, payload)
	}
}

func (s *Store) DeleteIDs(ctx context.Context, ids []string) error {
	seen := make(map[string]struct{})
	var unique []string
	for _, id := range ids {
		if id == "" {
			return vectorstore.ErrMissingDocumentID
		}
		if len(id) > nativeMaximumIDBytes {
			return errors.New("opensearch: document ID exceeds native 512-byte limit")
		}
		if _, duplicate := seen[id]; !duplicate {
			seen[id] = struct{}{}
			unique = append(unique, id)
		}
	}
	var prepared []bulkPublication
	for group := range slices.Chunk(unique, filterBatchSize) {
		publication := bulkPublication{ids: group}
		for _, id := range group {
			var err error
			publication.body, err = appendBulkLine(publication.body, bulkAction{Delete: &bulkActionTarget{Index: s.indexName, ID: id}})
			if err != nil {
				return err
			}
		}
		prepared = append(prepared, publication)
	}
	for _, publication := range prepared {
		if err := s.publish(ctx, publication, bulkOperationDelete, true); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) DeleteWhere(ctx context.Context, predicate filter.Predicate) error {
	if predicate == nil {
		return vectorstore.ErrMissingFilter
	}
	if err := predicate.Validate(); err != nil {
		return err
	}
	matches, err := s.selectMatches(ctx, predicate)
	if err != nil {
		return err
	}
	for group := range slices.Chunk(slices.Sorted(maps.Keys(matches)), filterBatchSize) {
		publication := bulkPublication{ids: group}
		for _, id := range group {
			hit := matches[id]
			publication.body, err = appendBulkLine(publication.body, bulkAction{Delete: &bulkActionTarget{Index: s.indexName, ID: id, SeqNo: hit.SeqNo, PrimaryTerm: hit.PrimaryTerm}})
			if err != nil {
				return err
			}
		}
		if err := s.publish(ctx, publication, bulkOperationDelete, false); err != nil {
			return err
		}
	}
	return nil
}

type httpFailure struct {
	status int
	body   string
}

func (h *httpFailure) Error() string {
	return fmt.Sprintf("opensearch: native HTTP %d: %s", h.status, h.body)
}

func appendBulkLine(body []byte, value any) ([]byte, error) {
	encoded, err := jsonv2.Marshal(value)
	if err != nil {
		return nil, err
	}
	body = append(body, encoded...)
	return append(body, '\n'), nil
}
