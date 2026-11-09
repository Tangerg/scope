package opensearch

import (
	"cmp"
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/Tangerg/scope/vectorstores/protocol/elastic"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/httpclient"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

var (
	_ vectorstore.Upserter  = (*Store)(nil)
	_ vectorstore.Querier   = (*Store)(nil)
	_ vectorstore.IDDeleter = (*Store)(nil)
)

type Store struct {
	client       *elastic.Client
	indexName    string
	schema       nativeSchema
	space        vectorstore.SpaceID
	maxBulkBytes int
}

func NewStore(ctx context.Context, config StoreConfig) (*Store, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	client, err := elastic.NewClient(elastic.ClientConfig{Perform: config.Client.Stream, MaxResponseBytes: config.MaxResponseBytes})
	if err != nil {
		return nil, err
	}
	store := &Store{client: client, indexName: cmp.Or(config.IndexName, elastic.DefaultIndexName), space: config.Space, maxBulkBytes: config.MaxBulkBytes}
	store.schema, err = store.readSchema(ctx)
	if err != nil {
		return nil, err
	}
	return store, nil
}

func (s *Store) readSchema(ctx context.Context) (nativeSchema, error) {
	raw, err := s.client.Read(ctx, http.MethodGet, "/"+s.indexName+"/_mapping", nil, nil, 0)
	if err != nil {
		if failure, ok := errors.AsType[*httpclient.ResponseError](err); ok && failure.HTTPStatus() == http.StatusNotFound {
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
	raw, err = s.client.Read(ctx, http.MethodGet, "/"+s.indexName+"/_settings", url.Values{"flat_settings": {"true"}, "include_defaults": {"true"}, "name": {"index.knn,index.derived_source.enabled,index.knn.derived_source.enabled,index.default_pipeline,index.final_pipeline,index.search.default_pipeline,index.max_result_window"}}, nil, 0)
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

func (s *Store) checkDocument(d *document.Document) error {
	if d.Media != nil {
		return vectorstore.ErrInvalidDocument
	}
	return elastic.DocumentID(d.ID).Validate()
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
	plan, err := elastic.NewBulkPlan(elastic.BulkConfig{Index: s.indexName, Operation: elastic.Index, MaxBytes: s.maxBulkBytes})
	if err != nil {
		return err
	}
	for _, r := range batch.Records {
		doc := r.Document
		if err := s.checkDocument(doc); err != nil {
			return err
		}
		coords, err := s.schema.indexVector(r.Values)
		if err != nil {
			return err
		}
		facts, err := doc.MarshalStoredMetadata()
		if err != nil {
			return err
		}
		if err := plan.Index(doc.ID, storedDocument{Content: doc.Text, MetadataJSON: string(facts), Embedding: coords}); err != nil {
			return err
		}
	}
	if err := s.verify(ctx); err != nil {
		return err
	}
	return plan.Publish(ctx, s.client)
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
func (s *Store) QueryByScan(ctx context.Context, q *vectorstore.Query, budget vectorstore.ScanBudget) (*vectorstore.SearchResponse, error) {
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
	if request.Options.Filter != nil && s.schema.engine == engineNMSLIB {
		return nil, fmt.Errorf("%w: opensearch: filtered KNN requires Lucene or Faiss", errors.ErrUnsupported)
	}
	if request.Options.ResultLimit() > s.schema.resultWindow {
		return nil, fmt.Errorf("%w: TopK exceeds native result window %d", vectorstore.ErrInvalidOptions, s.schema.resultWindow)
	}
	query, err := s.schema.vector(request.Values)
	if err != nil {
		return nil, err
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
	var matches map[string]elastic.Version
	if request.Options.Filter != nil {
		matches, err = s.selectMatches(ctx, request.Options.Filter, *budget)
		if err != nil {
			return nil, err
		}
		if len(matches) == 0 {
			return &vectorstore.SearchResponse{}, nil
		}
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
		raw, err := s.client.Read(ctx, http.MethodPost, "/"+s.indexName+"/_search", url.Values{"allow_partial_search_results": {"false"}}, body, 0)
		if err != nil {
			return nil, err
		}
		var page elastic.SearchResponse
		if err := jsonv2.Unmarshal(raw, &page); err != nil {
			return nil, err
		}
		if err := page.Validate(s.indexName); err != nil {
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

func (s *Store) selectMatches(ctx context.Context, predicate filter.Predicate, budget vectorstore.ScanBudget) (map[string]elastic.Version, error) {
	body, err := jsonv2.Marshal(searchRequest{Size: min(filterBatchSize, budget.MaxRecords), TrackTotalHits: true, SeqNoPrimaryTerm: true, StoredFields: []string{"_routing"}, Sort: []string{"_doc"}, Source: true})
	if err != nil {
		return nil, err
	}
	scroll, err := elastic.NewScroll(elastic.ScrollConfig{Client: s.client, Index: s.indexName, Request: body, Budget: budget, Decode: s.schema.decode})
	if err != nil {
		return nil, err
	}
	return scroll.Select(ctx, predicate)
}

func (s *Store) DeleteIDs(ctx context.Context, ids []string) error {
	seen := make(map[string]struct{})
	var unique []string
	for _, id := range ids {
		if err := elastic.DocumentID(id).Validate(); err != nil {
			return err
		}
		if _, duplicate := seen[id]; !duplicate {
			seen[id] = struct{}{}
			unique = append(unique, id)
		}
	}
	if len(unique) == 0 {
		return nil
	}
	plan, err := elastic.NewBulkPlan(elastic.BulkConfig{Index: s.indexName, Operation: elastic.Delete, MaxBytes: s.maxBulkBytes, AllowMissing: true})
	if err != nil {
		return err
	}
	for _, id := range unique {
		if err := plan.Delete(id, nil); err != nil {
			return err
		}
	}
	if err := s.verify(ctx); err != nil {
		return err
	}
	return plan.Publish(ctx, s.client)
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
	plan, err := elastic.NewBulkPlan(elastic.BulkConfig{Index: s.indexName, Operation: elastic.Delete, MaxBytes: s.maxBulkBytes})
	if err != nil {
		return err
	}
	for _, id := range slices.Sorted(maps.Keys(matches)) {
		version := matches[id]
		if err := plan.Delete(id, &version); err != nil {
			return err
		}
	}
	return plan.Publish(ctx, s.client)
}
