package elastic

import (
	"cmp"
	"context"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/httpclient"
	"github.com/Tangerg/scope/core/vector"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

const (
	maxResultWindowSetting = "index.max_result_window"
	// Both engines cap native nearest-neighbor k at this value.
	nativeMaximumK = 10000
	// filterBatchSize bounds the candidate IDs of one filtered search.
	filterBatchSize = 512
)

var (
	_ vectorstore.Upserter  = (*Store)(nil)
	_ vectorstore.Querier   = (*Store)(nil)
	_ vectorstore.IDDeleter = (*Store)(nil)
)

type StoreConfig struct {
	Engine  Engine
	Perform func(*http.Request) (*http.Response, error)
	// IndexName names one concrete index, never an alias.
	IndexName string
	Space     vectorstore.SpaceID
	// MaxBulkBytes limits the encoded action/document lines per native write.
	MaxBulkBytes     int
	MaxResponseBytes int64
}

func (s StoreConfig) Validate() error {
	if lo.IsNil(s.Engine) {
		return errors.New("elastic: Engine is required")
	}
	if err := s.Space.Validate(); err != nil {
		return err
	}
	if err := s.client().Validate(); err != nil {
		return err
	}
	return s.bulk(Index).Validate()
}

func (s StoreConfig) client() ClientConfig {
	return ClientConfig{Perform: s.Perform, MaxResponseBytes: s.MaxResponseBytes}
}

func (s StoreConfig) bulk(operation Operation) BulkConfig {
	return BulkConfig{Index: cmp.Or(s.IndexName, DefaultIndexName), Operation: operation, MaxBytes: s.MaxBulkBytes}
}

// Store implements the Core vector-store capabilities over one host-provisioned
// index. It never creates or modifies the index; it re-admits the native mapping
// and settings before every mutation and query so a changed index cannot
// receive or answer with records under the admitted schema.
type Store struct {
	client *Client
	config StoreConfig
	index  admittedIndex
}

// admittedIndex is what Store re-reads and compares before every request.
type admittedIndex struct {
	schema       Schema
	resultWindow int
}

func NewStore(ctx context.Context, config StoreConfig) (*Store, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	client, err := NewClient(config.client())
	if err != nil {
		return nil, err
	}
	config.IndexName = cmp.Or(config.IndexName, DefaultIndexName)
	store := &Store{client: client, config: config}
	store.index, err = store.admit(ctx)
	if err != nil {
		return nil, err
	}
	if !reflect.TypeOf(store.index.schema).Comparable() {
		return nil, fmt.Errorf("%s: engine schema %T is not comparable", config.Engine.Name(), store.index.schema)
	}
	return store, nil
}

func (s *Store) admit(ctx context.Context) (admittedIndex, error) {
	raw, err := s.client.Read(ctx, http.MethodGet, "/"+s.config.IndexName+"/_mapping", nil, nil, 0)
	if err != nil {
		if failure, ok := errors.AsType[*httpclient.ResponseError](err); ok && failure.HTTPStatus() == http.StatusNotFound {
			return admittedIndex{}, fmt.Errorf("%w: %w", ErrIndexMissing, err)
		}
		return admittedIndex{}, err
	}
	var mappings map[string]struct {
		Mappings indexMapping `json:"mappings"`
	}
	if err = jsonv2.Unmarshal(raw, &mappings); err != nil {
		return admittedIndex{}, err
	}
	mapping, exists := mappings[s.config.IndexName]
	if !exists || len(mappings) != 1 {
		return admittedIndex{}, fmt.Errorf("%w: requires one concrete index without aliases", ErrIncompatibleIndex)
	}
	query := url.Values{"flat_settings": {"true"}, "include_defaults": {"true"}, "name": {settingNames(s.config.Engine.SettingNames())}}
	raw, err = s.client.Read(ctx, http.MethodGet, "/"+s.config.IndexName+"/_settings", query, nil, 0)
	if err != nil {
		return admittedIndex{}, err
	}
	var settings map[string]IndexSettings
	if err = jsonv2.Unmarshal(raw, &settings); err != nil {
		return admittedIndex{}, err
	}
	native, exists := settings[s.config.IndexName]
	if !exists || len(settings) != 1 {
		return admittedIndex{}, fmt.Errorf("%w: settings do not describe the concrete index", ErrIncompatibleIndex)
	}
	if mapping.Mappings.Meta.Space != string(s.config.Space) {
		return admittedIndex{}, vectorstore.ErrIncompatibleSpace
	}
	if err = mapping.Mappings.admitRecordLayout(); err != nil {
		return admittedIndex{}, err
	}
	schema, err := s.config.Engine.Admit(mapping.Mappings.Properties[EmbeddingField], native)
	if err != nil {
		return admittedIndex{}, err
	}
	limit, err := strconv.Atoi(native.Value(maxResultWindowSetting))
	if err != nil || limit <= 0 {
		return admittedIndex{}, fmt.Errorf("%w: invalid native result window", ErrIncompatibleIndex)
	}
	return admittedIndex{schema: schema, resultWindow: min(limit, nativeMaximumK)}, nil
}

func (s *Store) verify(ctx context.Context) error {
	current, err := s.admit(ctx)
	if err != nil {
		return err
	}
	if current != s.index {
		return ErrIncompatibleIndex
	}
	return nil
}

func (s *Store) checkDocument(d *document.Document) error {
	if d.Media != nil {
		return vectorstore.ErrInvalidDocument
	}
	return DocumentID(d.ID).Validate()
}

func (s *Store) CheckDocuments(ctx context.Context, space vectorstore.SpaceID, docs []*document.Document) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := space.Require(s.config.Space); err != nil {
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
	if err := batch.ValidateFor(s.config.Space); err != nil {
		return err
	}
	if err := vectorstore.RequireWidth(batch.Records[0].Values, s.index.schema.Dimensions()); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	plan, err := NewBulkPlan(s.config.bulk(Index))
	if err != nil {
		return err
	}
	for _, r := range batch.Records {
		doc := r.Document
		if err := s.checkDocument(doc); err != nil {
			return err
		}
		coords := vector.Float32(r.Values)
		if err := s.index.schema.ValidateIndexedVector(coords); err != nil {
			return err
		}
		facts, err := doc.MarshalStoredMetadata()
		if err != nil {
			return err
		}
		if err := plan.Index(doc.ID, StoredDocument{Content: doc.Text, MetadataJSON: string(facts), Embedding: coords}); err != nil {
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
	if err := space.Require(s.config.Space); err != nil {
		return err
	}
	if err := opts.Validate(); err != nil {
		return err
	}
	if err := s.admitOptions(opts); err != nil {
		return err
	}
	if opts.Filter != nil {
		return vectorstore.ErrUnsupportedFilter
	}
	return s.verify(ctx)
}

// admitOptions holds validated options to what one native kNN search can answer.
func (s *Store) admitOptions(opts vectorstore.SearchOptions) error {
	if err := opts.RequireMode(vectorstore.SearchModeSemantic); err != nil {
		return err
	}
	return opts.RequireResultLimit(s.index.resultWindow)
}

// Query only sends native nearest-neighbor searches; the mapping keeps metadata
// opaque, so a filtered Query is unsupported rather than silently scanned.
func (s *Store) Query(ctx context.Context, q *vectorstore.Query) (*vectorstore.SearchResponse, error) {
	if err := q.ValidateFor(s.config.Space); err != nil {
		return nil, err
	}
	if q.Options.Filter != nil {
		return nil, vectorstore.ErrUnsupportedFilter
	}
	return s.rank(ctx, q, nil)
}

// QueryByScan interprets the filter over a budgeted scroll of the whole index,
// then ranks only the selected records.
func (s *Store) QueryByScan(ctx context.Context, q *vectorstore.Query, budget vectorstore.ScanBudget) (*vectorstore.SearchResponse, error) {
	if err := q.ValidateFor(s.config.Space); err != nil {
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

type rankedResult struct {
	result *vectorstore.SearchResult
	// native orders results across candidate groups before Core normalization
	// can saturate distinct native scores.
	native float64
}

func (s *Store) rank(ctx context.Context, request *vectorstore.Query, budget *vectorstore.ScanBudget) (response *vectorstore.SearchResponse, err error) {
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if err = s.admitOptions(request.Options); err != nil {
		return nil, err
	}
	if request.Options.Filter != nil {
		if err = s.index.schema.CheckFilteredSearch(); err != nil {
			return nil, err
		}
	}
	if err = vectorstore.RequireWidth(request.Values, s.index.schema.Dimensions()); err != nil {
		return nil, err
	}
	query := vector.Float32(request.Values)
	if err = s.index.schema.ValidateQueryVector(query); err != nil {
		return nil, err
	}
	if err = s.verify(ctx); err != nil {
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
	groups := [][]string{nil}
	if request.Options.Filter != nil {
		matches, selectErr := s.selectMatches(ctx, request.Options.Filter, *budget)
		if selectErr != nil {
			return nil, selectErr
		}
		if len(matches) == 0 {
			return &vectorstore.SearchResponse{}, nil
		}
		groups = slices.Collect(slices.Chunk(slices.Sorted(maps.Keys(matches)), filterBatchSize))
	}
	var ranked []rankedResult
	seen := make(map[string]struct{})
	for _, ids := range groups {
		results, searchErr := s.search(ctx, request, &Neighbors{Vector: query, K: request.Options.ResultLimit(), IDs: ids}, seen)
		if searchErr != nil {
			return nil, searchErr
		}
		ranked = append(ranked, results...)
	}
	slices.SortFunc(ranked, func(left, right rankedResult) int {
		if order := cmp.Compare(right.native, left.native); order != 0 {
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

// search ranks one candidate group. Every hit must be new across groups and,
// for a filtered query, both a selected candidate and a current filter match.
func (s *Store) search(ctx context.Context, request *vectorstore.Query, neighbors *Neighbors, seen map[string]struct{}) ([]rankedResult, error) {
	body, err := s.searchBody(neighbors.K, neighbors)
	if err != nil {
		return nil, err
	}
	raw, err := s.client.Read(ctx, http.MethodPost, "/"+s.config.IndexName+"/_search", url.Values{"allow_partial_search_results": {"false"}}, body, 0)
	if err != nil {
		return nil, err
	}
	var page SearchResponse
	if err := jsonv2.Unmarshal(raw, &page); err != nil {
		return nil, err
	}
	if err := page.Validate(s.config.IndexName); err != nil {
		return nil, err
	}
	hits := *page.Hits.Hits
	if int64(len(hits)) != min(*page.Hits.Total.Value, int64(neighbors.K)) {
		return nil, s.failure("native search did not acknowledge every hit")
	}
	results := make([]rankedResult, 0, len(hits))
	for _, hit := range hits {
		if _, duplicate := seen[hit.ID]; duplicate {
			return nil, s.failure("native search repeated a document ID")
		}
		seen[hit.ID] = struct{}{}
		doc, err := decodeRecord(hit, s.index.schema)
		if err != nil {
			return nil, err
		}
		if membershipErr := s.checkMembership(request.Options.Filter, neighbors.IDs, hit.ID, doc); membershipErr != nil {
			return nil, membershipErr
		}
		if hit.Score == nil {
			return nil, s.failure("native hit is missing its score")
		}
		score, err := s.index.schema.Score(*hit.Score)
		if err != nil {
			return nil, err
		}
		result, err := vectorstore.NewSearchResult(doc, score)
		if err != nil {
			return nil, err
		}
		results = append(results, rankedResult{result: result, native: *hit.Score})
	}
	return results, nil
}

func (s *Store) checkMembership(predicate filter.Predicate, candidates []string, id string, doc *document.Document) error {
	if predicate == nil {
		return nil
	}
	if !slices.Contains(candidates, id) {
		return s.failure("native hit is outside Core membership")
	}
	matched, err := filter.Match(predicate, doc.Metadata)
	if err != nil {
		return err
	}
	if !matched {
		return s.failure("native hit changed Core membership")
	}
	return nil
}

func (s *Store) failure(message string) error {
	return fmt.Errorf("%s: %s", s.config.Engine.Name(), message)
}

// searchBody joins Store's request members, which its response checks rely on,
// with the engine's projection and nearest-neighbor syntax.
func (s *Store) searchBody(size int, neighbors *Neighbors) ([]byte, error) {
	syntax, err := s.index.schema.SearchSyntax(neighbors)
	if err != nil {
		return nil, err
	}
	request := struct {
		Size             int            `json:"size"`
		TrackTotalHits   bool           `json:"track_total_hits"`
		SeqNoPrimaryTerm bool           `json:"seq_no_primary_term"`
		StoredFields     []string       `json:"stored_fields"`
		Sort             []string       `json:"sort,omitzero"`
		Syntax           jsontext.Value `json:",embed"`
	}{Size: size, TrackTotalHits: true, SeqNoPrimaryTerm: true, StoredFields: []string{routingField}, Syntax: syntax}
	if neighbors == nil {
		request.Sort = []string{"_doc"}
	}
	return jsonv2.Marshal(request)
}

func (s *Store) selectMatches(ctx context.Context, predicate filter.Predicate, budget vectorstore.ScanBudget) (map[string]Version, error) {
	body, err := s.searchBody(min(filterBatchSize, budget.MaxRecords), nil)
	if err != nil {
		return nil, err
	}
	decode := func(hit Hit) (*document.Document, error) { return decodeRecord(hit, s.index.schema) }
	scroll, err := NewScroll(ScrollConfig{Client: s.client, Index: s.config.IndexName, Request: body, Budget: budget, Decode: decode})
	if err != nil {
		return nil, err
	}
	return scroll.Select(ctx, predicate)
}

func (s *Store) DeleteIDs(ctx context.Context, ids []string) error {
	unique, err := vectorstore.DistinctIDs(ids)
	if err != nil {
		return err
	}
	for _, id := range unique {
		if err = DocumentID(id).Validate(); err != nil {
			return err
		}
	}
	if len(unique) == 0 {
		return nil
	}
	plan, err := NewBulkPlan(s.config.bulk(Delete))
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

// DeleteMatching completes selection first and deletes each selected record
// only at the version it was selected at; a conflict is an error.
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
	plan, err := NewBulkPlan(s.config.bulk(Delete))
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
