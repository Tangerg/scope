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

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

var (
	_ vectorstore.Upserter  = (*Store)(nil)
	_ vectorstore.Querier   = (*Store)(nil)
	_ vectorstore.IDDeleter = (*Store)(nil)
)

type Store struct {
	client           APIClient
	indexName        string
	schema           nativeSchema
	space            vectorstore.SpaceID
	maxBulkBytes     int
	maxResponseBytes int64
}

func NewStore(ctx context.Context, config StoreConfig) (*Store, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	store := &Store{client: config.Client, indexName: cmp.Or(config.IndexName, DefaultIndexName), space: config.Space, maxBulkBytes: cmp.Or(config.MaxBulkBytes, DefaultMaxBulkBytes), maxResponseBytes: cmp.Or(config.MaxResponseBytes, DefaultMaxResponseBytes)}
	var err error
	store.schema, err = store.readSchema(ctx)
	if err != nil {
		return nil, err
	}
	return store, nil
}

func (s *Store) readSchema(ctx context.Context) (nativeSchema, error) {
	raw, err := s.request(ctx, http.MethodGet, "/"+s.indexName+"/_mapping", nil, nil)
	if err != nil {
		if failure, ok := errors.AsType[*httpFailure](err); ok && failure.status == http.StatusNotFound {
			return nativeSchema{}, fmt.Errorf("%w: %w", ErrIndexMissing, err)
		}
		return nativeSchema{}, err
	}
	var mappings map[string]mappedIndex
	if err = jsonv2.Unmarshal(raw, &mappings); err != nil {
		return nativeSchema{}, err
	}
	mapping, exists := mappings[s.indexName]
	if !exists || len(mappings) != 1 {
		return nativeSchema{}, fmt.Errorf("%w: requires one concrete index without aliases", ErrIncompatibleIndex)
	}
	raw, err = s.request(ctx, http.MethodGet, "/"+s.indexName+"/_settings", url.Values{"flat_settings": {"true"}, "include_defaults": {"true"}, "name": {"index.knn,index.derived_source.enabled,index.knn.derived_source.enabled,index.default_pipeline,index.final_pipeline,index.search.default_pipeline,index.max_result_window"}}, nil)
	if err != nil {
		return nativeSchema{}, err
	}
	var settings map[string]nativeSettings
	if err = jsonv2.Unmarshal(raw, &settings); err != nil {
		return nativeSchema{}, err
	}
	native, exists := settings[s.indexName]
	if !exists || len(settings) != 1 {
		return nativeSchema{}, fmt.Errorf("%w: settings do not describe the concrete index", ErrIncompatibleIndex)
	}
	if mapping.Mappings.Meta.Space != string(s.space) {
		return nativeSchema{}, vectorstore.ErrIncompatibleSpace
	}
	return mapping.Mappings.schema(native)
}
func (s *Store) verify(ctx context.Context) error {
	current, err := s.readSchema(ctx)
	if err != nil {
		return err
	}
	if current != s.schema {
		return ErrIncompatibleIndex
	}
	return nil
}

func (s *Store) request(ctx context.Context, method, path string, query url.Values, body []byte) ([]byte, error) {
	return s.read(ctx, method, path, query, body, s.maxResponseBytes)
}
func (s *Store) read(ctx context.Context, method, path string, query url.Values, body []byte, limit int64) (raw []byte, err error) {
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
	raw, err = io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return raw, err
	}
	if int64(len(raw)) > limit {
		if limit < s.maxResponseBytes {
			return raw, vectorstore.ErrScanBudget
		}
		return raw, errors.New("opensearch: native response exceeds MaxResponseBytes")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return raw, &httpFailure{status: response.StatusCode, body: string(raw)}
	}
	return raw, nil
}

func (s *Store) checkDocument(d *document.Document) error {
	if d.Media != nil {
		return vectorstore.ErrInvalidDocument
	}
	if len(d.ID) > nativeMaximumIDBytes {
		return errors.New("opensearch: document ID exceeds native 512-byte limit")
	}
	return nil
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
	for _, d := range docs {
		if err := s.checkDocument(d); err != nil {
			return err
		}
	}
	return s.verify(ctx)
}
func (s *Store) Upsert(ctx context.Context, batch *vectorstore.Batch) error {
	if err := batch.ValidateFor(s.space); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	var groups []bulkPublication
	var pending bulkPublication
	for _, r := range batch.Records {
		doc := r.Document
		if err := s.checkDocument(doc); err != nil {
			return err
		}
		coords, err := s.schema.indexVector(r.Values)
		if err != nil {
			return err
		}
		facts, err := document.MarshalStoredMetadata(doc)
		if err != nil {
			return err
		}
		item, err := appendBulkLine(nil, bulkAction{Index: &bulkActionTarget{Index: s.indexName, ID: doc.ID}})
		if err != nil {
			return err
		}
		item, err = appendBulkLine(item, storedDocument{Content: doc.Text, MetadataJSON: string(facts), Embedding: coords})
		if err != nil {
			return err
		}
		if len(item) > s.maxBulkBytes {
			return fmt.Errorf("opensearch: encoded record %q exceeds MaxBulkBytes", doc.ID)
		}
		if len(item) > s.maxBulkBytes-len(pending.body) {
			groups = append(groups, pending)
			pending = bulkPublication{}
		}
		pending.ids = append(pending.ids, doc.ID)
		pending.body = append(pending.body, item...)
	}
	groups = append(groups, pending)
	if err := s.verify(ctx); err != nil {
		return err
	}
	for i, g := range groups {
		if err := s.publish(ctx, g, bulkOperationIndex, false); err != nil {
			w, ok := errors.AsType[*vectorstore.WriteError](err)
			if !ok {
				w = &vectorstore.WriteError{Err: err, Unconfirmed: slices.Clone(g.ids)}
			}
			for _, prior := range groups[:i] {
				w.Acknowledged = append(w.Acknowledged, prior.ids...)
			}
			for _, later := range groups[i+1:] {
				w.Unattempted = append(w.Unattempted, later.ids...)
			}
			return w
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
	if decodeErr := jsonv2.Unmarshal(raw, &response); decodeErr != nil {
		return decodeErr
	}
	err = response.validate(operation, s.indexName, publication.ids, allowMissing)
	if err == nil || operation != bulkOperationIndex {
		return err
	}
	return response.writeFailure(s.indexName, publication.ids, err)
}

func (s *Store) CheckQuery(ctx context.Context, space vectorstore.SpaceID, opts vectorstore.SearchOptions) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := space.Require(s.space); err != nil {
		return err
	}
	if err := opts.RequireMode(vectorstore.SearchModeSemantic); err != nil {
		return err
	}
	if opts.Filter != nil {
		return vectorstore.ErrUnsupportedFilter
	}
	if opts.ResultLimit() > s.schema.resultWindow {
		return vectorstore.ErrInvalidOptions
	}
	return s.verify(ctx)
}
func (s *Store) Query(ctx context.Context, q *vectorstore.Query) (*vectorstore.SearchResponse, error) {
	if err := q.ValidateFor(s.space); err != nil {
		return nil, err
	}
	if q.Options.Filter != nil {
		return nil, vectorstore.ErrUnsupportedFilter
	}
	return s.rank(ctx, q, nil)
}
func (s *Store) Scan(ctx context.Context, q *vectorstore.Query, budget vectorstore.ScanBudget) (*vectorstore.SearchResponse, error) {
	if err := q.ValidateFor(s.space); err != nil {
		return nil, err
	}
	if q.Options.Filter == nil {
		return nil, vectorstore.ErrMissingFilter
	}
	if err := budget.Validate(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, budget.Timeout)
	defer cancel()
	return s.rank(ctx, q, &budget)
}
func (s *Store) rank(ctx context.Context, request *vectorstore.Query, budget *vectorstore.ScanBudget) (response *vectorstore.SearchResponse, err error) {
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if err = request.ValidateFor(s.space); err != nil {
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
	if verifyErr := s.verify(ctx); verifyErr != nil {
		return nil, verifyErr
	}
	defer func() {
		if err == nil {
			err = request.ValidateResponse(response)
		}
		if err != nil {
			response = nil
		}
	}()
	var matches map[string]searchHit
	if request.Options.Filter != nil {
		matches, err = s.selectMatches(ctx, request.Options.Filter, *budget)
		if err != nil {
			return nil, err
		}
		if len(matches) == 0 {
			return &vectorstore.SearchResponse{}, nil
		}
	}
	query, err := s.schema.vector(request.Values)
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

func (s *Store) selectMatches(ctx context.Context, predicate filter.Predicate, budget vectorstore.ScanBudget) (selected map[string]searchHit, err error) {
	body, err := jsonv2.Marshal(searchRequest{Size: min(filterBatchSize, budget.MaxRecords), Source: true, TrackTotalHits: true, SeqNoPrimaryTerm: true, StoredFields: []string{"_routing"}, Sort: []string{"_doc"}})
	if err != nil {
		return nil, err
	}
	bytesLeft := budget.MaxBytes
	raw, callErr := s.read(ctx, http.MethodPost, "/"+s.indexName+"/_search", url.Values{"scroll": {"1m"}, "allow_partial_search_results": {"false"}}, body, min(bytesLeft, s.maxResponseBytes))
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
		bytesLeft -= int64(len(raw))
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
			if total > int64(budget.MaxRecords) {
				return nil, vectorstore.ErrScanBudget
			}
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
		if bytesLeft <= 0 {
			return nil, vectorstore.ErrScanBudget
		}
		raw, callErr = s.read(ctx, http.MethodPost, "/_search/scroll", nil, payload, min(bytesLeft, s.maxResponseBytes))
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
	if len(unique) == 0 {
		return nil
	}
	if err := s.verify(ctx); err != nil {
		return err
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

func (s *Store) DeleteMatching(ctx context.Context, predicate filter.Predicate, budget vectorstore.ScanBudget) error {
	if err := budget.Validate(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, budget.Timeout)
	defer cancel()
	if predicate == nil {
		return vectorstore.ErrMissingFilter
	}
	if err := predicate.Validate(); err != nil {
		return err
	}
	if err := s.verify(ctx); err != nil {
		return err
	}
	matches, err := s.selectMatches(ctx, predicate, budget)
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
