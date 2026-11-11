package milvus

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/milvus-io/milvus/client/v3/entity"
	"github.com/milvus-io/milvus/client/v3/index"
	"github.com/milvus-io/milvus/client/v3/milvusclient"
	"google.golang.org/grpc"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

var (
	_ vectorstore.Upserter  = (*Store)(nil)
	_ vectorstore.Querier   = (*Store)(nil)
	_ vectorstore.IDDeleter = (*Store)(nil)
)

type collectionClient interface {
	DescribeCollection(context.Context, milvusclient.DescribeCollectionOption, ...grpc.CallOption) (*entity.Collection, error)
	ListIndexes(context.Context, milvusclient.ListIndexOption, ...grpc.CallOption) ([]string, error)
	DescribeIndex(context.Context, milvusclient.DescribeIndexOption, ...grpc.CallOption) (milvusclient.IndexDescription, error)
	Upsert(context.Context, milvusclient.UpsertOption, ...grpc.CallOption) (milvusclient.UpsertResult, error)
	Search(context.Context, milvusclient.SearchOption, ...grpc.CallOption) ([]milvusclient.ResultSet, error)
	Query(context.Context, milvusclient.QueryOption, ...grpc.CallOption) (milvusclient.ResultSet, error)
	Delete(context.Context, milvusclient.DeleteOption, ...grpc.CallOption) (milvusclient.DeleteResult, error)
}

type Store struct {
	client         collectionClient
	space          vectorstore.SpaceID
	collectionName string
}

func NewStore(ctx context.Context, config StoreConfig) (*Store, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	s := &Store{client: config.Client, space: config.Space, collectionName: config.CollectionName}
	if _, err := s.readPolicy(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) readPolicy(ctx context.Context) (nativePolicy, error) {
	collection, err := s.client.DescribeCollection(ctx, milvusclient.NewDescribeCollectionOption(s.collectionName))
	if err != nil {
		return nativePolicy{}, err
	}
	if collection == nil || collection.Schema == nil || collection.Schema.Description != s.space.StorageKey() {
		return nativePolicy{}, fmt.Errorf("%w: incorrect physical space binding", ErrSchemaMismatch)
	}
	policy, err := decodePolicy(collection, s.collectionName)
	if err != nil {
		return nativePolicy{}, err
	}
	names, err := s.client.ListIndexes(ctx, milvusclient.NewListIndexOption(s.collectionName).WithFieldName(fieldVector))
	if err != nil {
		return nativePolicy{}, err
	}
	if len(names) != 1 || names[0] == "" {
		return nativePolicy{}, fmt.Errorf("%w: exactly one named vector index is required", ErrSchemaMismatch)
	}
	description, err := s.client.DescribeIndex(ctx, milvusclient.NewDescribeIndexOption(s.collectionName, names[0]))
	if err != nil {
		return nativePolicy{}, err
	}
	if err = policy.readMetric(description); err != nil {
		return nativePolicy{}, err
	}
	return policy, nil
}

func (s *Store) selectDocuments(ctx context.Context, policy nativePolicy, predicate filter.Predicate, meter *vectorstore.ScanMeter) ([]nativeRecord, error) {
	var selected []nativeRecord
	cursor := ""
	for {
		rows, err := s.client.Query(ctx, sourcePage{collection: s.collectionName, after: cursor})
		if err != nil {
			return nil, err
		}
		records, err := decodeRows(rows, policy)
		if err != nil {
			return nil, err
		}
		if len(records) > sourcePageSize {
			return nil, errors.New("milvus: source page exceeds its limit")
		}
		if len(records) == 0 {
			return selected, nil
		}
		for _, record := range records {
			if err := meter.Charge(int64(len(record.doc.ID)) + int64(len(record.doc.Text)) + int64(len(record.metadata)) + 4*int64(policy.dimensions)); err != nil {
				return nil, err
			}
			if record.doc.ID <= cursor {
				return nil, errors.New("milvus: source identities do not advance")
			}
			cursor = record.doc.ID
			matches, err := matchesMetadata(predicate, record.doc)
			if err != nil {
				return nil, err
			}
			if matches {
				selected = append(selected, record)
			}
		}
	}
}

func (s *Store) CheckDocuments(ctx context.Context, space vectorstore.SpaceID, docs []*document.Document) error {
	if err := space.Require(s.space); err != nil {
		return err
	}
	if err := vectorstore.ValidateDocuments(docs); err != nil {
		return err
	}
	for _, doc := range docs {
		if doc.Media != nil {
			return vectorstore.ErrInvalidDocument
		}
	}
	policy, err := s.readPolicy(ctx)
	if err != nil {
		return err
	}
	_, err = encodeDocuments(docs, policy)
	return err
}

func (s *Store) Upsert(ctx context.Context, batch *vectorstore.Batch) error {
	if err := batch.ValidateFor(s.space); err != nil {
		return err
	}
	docs := make([]*document.Document, len(batch.Records))
	values := make([][]float64, len(docs))
	ids := make([]string, len(docs))
	for i, r := range batch.Records {
		if r.Document.Media != nil {
			return vectorstore.ErrInvalidDocument
		}
		docs[i] = r.Document
		values[i] = r.Values
		ids[i] = r.Document.ID
	}
	policy, err := s.readPolicy(ctx)
	if err != nil {
		return err
	}
	records, err := encodeDocuments(docs, policy)
	if err != nil {
		return err
	}
	cols, err := encodeColumns(records, values, policy)
	if err != nil {
		return err
	}
	result, err := s.client.Upsert(ctx, milvusclient.NewColumnBasedInsertOption(s.collectionName, cols...))
	if err != nil {
		return vectorstore.NewWriteError(err, vectorstore.WriteOutcome{Unconfirmed: ids})
	}
	if result.UpsertCount != int64(len(docs)) || result.IDs == nil || result.IDs.Len() != len(docs) {
		return vectorstore.NewWriteError(errors.New("milvus: inconsistent upsert acknowledgment"), vectorstore.WriteOutcome{Unconfirmed: ids})
	}
	seen := make(map[string]bool, len(ids))
	for i := range ids {
		id, err := result.IDs.GetAsString(i)
		if err != nil {
			return vectorstore.NewWriteError(err, vectorstore.WriteOutcome{Unconfirmed: ids})
		}
		seen[id] = true
	}
	for _, id := range ids {
		if !seen[id] {
			return vectorstore.NewWriteError(errors.New("milvus: acknowledged IDs differ from submitted records"), vectorstore.WriteOutcome{Unconfirmed: ids})
		}
	}
	return nil
}

func (s *Store) CheckQuery(ctx context.Context, space vectorstore.SpaceID, opts vectorstore.SearchOptions) error {
	if err := space.Require(s.space); err != nil {
		return err
	}
	if err := s.checkOptions(opts, false); err != nil {
		return err
	}
	_, err := s.readPolicy(ctx)
	return err
}

func (s *Store) checkOptions(opts vectorstore.SearchOptions, allowFilter bool) error {
	if err := opts.Validate(); err != nil {
		return err
	}
	if err := opts.RequireMode(vectorstore.SearchModeSemantic); err != nil {
		return err
	}
	if opts.ResultLimit() > nativeMaxTopK {
		return errors.New("milvus: result limit exceeds the native topK limit")
	}
	if opts.Filter != nil && !allowFilter {
		return vectorstore.ErrUnsupportedFilter
	}
	return nil
}

func (s *Store) Query(ctx context.Context, q *vectorstore.Query) (*vectorstore.SearchResponse, error) {
	return s.rank(ctx, q, nil)
}

func (s *Store) QueryByScan(ctx context.Context, q *vectorstore.Query, budget vectorstore.ScanBudget) (*vectorstore.SearchResponse, error) {
	meter, err := budget.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer meter.Close()
	return s.rank(meter.Context(), q, meter)
}

func (s *Store) rank(ctx context.Context, request *vectorstore.Query, meter *vectorstore.ScanMeter) (*vectorstore.SearchResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := request.ValidateFor(s.space); err != nil {
		return nil, err
	}
	if err := s.checkOptions(request.Options, meter != nil); err != nil {
		return nil, err
	}
	policy, err := s.readPolicy(ctx)
	if err != nil {
		return nil, err
	}
	query, err := policy.vector(request.Values)
	if err != nil {
		return nil, err
	}
	groups, err := s.identityGroups(ctx, policy, request.Options.Filter, meter)
	if err != nil {
		return nil, err
	}
	var ranked []rankedDocument
	for _, group := range groups {
		hits, err := s.searchGroup(ctx, policy, query, group, request.Options)
		if err != nil {
			return nil, err
		}
		ranked = append(ranked, hits...)
	}
	slices.SortFunc(ranked, func(left, right rankedDocument) int {
		return cmp.Or(policy.compareRaw(left.raw, right.raw), cmp.Compare(left.result.Document.ID, right.result.Document.ID))
	})
	response := &vectorstore.SearchResponse{}
	for _, hit := range ranked[:min(request.Options.ResultLimit(), len(ranked))] {
		response.Results = append(response.Results, hit.result)
	}
	if err := request.ValidateResponse(response); err != nil {
		return nil, err
	}
	return response, nil
}

// identityGroups bounds each native search: one unrestricted group without a
// filter, otherwise the selected identities in groups the filter template admits.
func (s *Store) identityGroups(ctx context.Context, policy nativePolicy, predicate filter.Predicate, meter *vectorstore.ScanMeter) ([][]string, error) {
	if predicate == nil {
		return [][]string{nil}, nil
	}
	candidates, err := s.selectDocuments(ctx, policy, predicate, meter)
	if err != nil {
		return nil, err
	}
	ids := make([]string, len(candidates))
	for index, record := range candidates {
		ids[index] = record.doc.ID
	}
	return slices.Collect(slices.Chunk(ids, identityGroupSize)), nil
}

// searchGroup ranks one identity group natively and admits each hit only when
// its identity, membership and metadata still agree with the selection.
func (s *Store) searchGroup(ctx context.Context, policy nativePolicy, query []float32, ids []string, options vectorstore.SearchOptions) ([]rankedDocument, error) {
	limit := options.ResultLimit()
	if ids != nil {
		limit = min(limit, len(ids))
	}
	search := milvusclient.NewSearchOption(s.collectionName, limit, []entity.Vector{entity.FloatVector(query)}).
		WithANNSField(fieldVector).WithOutputFields(fieldID, fieldContent, fieldMeta, fieldVector).
		WithConsistencyLevel(entity.ClStrong).
		WithSearchParam(index.MetricTypeKey, string(policy.metric))
	if ids != nil {
		search = search.WithFilter(identityFilter).WithTemplateParam(identitiesParam, ids)
	}
	results, err := s.client.Search(ctx, search)
	if err != nil {
		return nil, err
	}
	if len(results) != 1 {
		return nil, errors.New("milvus: search must return exactly one query result")
	}
	rows := results[0]
	records, err := decodeRows(rows, policy)
	if err != nil {
		return nil, err
	}
	if len(rows.Scores) != len(records) || len(records) > limit || len(records) != 0 && rows.IDs == nil || rows.IDs != nil && rows.IDs.Len() != len(records) {
		return nil, errors.New("milvus: search result shape is inconsistent")
	}
	var ranked []rankedDocument
	seen := make(map[string]bool, len(records))
	for position, record := range records {
		nativeID, err := rows.IDs.GetAsString(position)
		if err != nil {
			return nil, err
		}
		if nativeID != record.doc.ID || ids != nil && !slices.Contains(ids, record.doc.ID) || seen[record.doc.ID] {
			return nil, errors.New("milvus: search identity is inconsistent")
		}
		seen[record.doc.ID] = true
		hit, admitted, err := admitHit(policy, options, record, float64(rows.Scores[position]))
		if err != nil {
			return nil, err
		}
		if admitted {
			ranked = append(ranked, hit)
		}
	}
	return ranked, nil
}

// admitHit rechecks the filter against the returned metadata and drops a hit
// below the minimum score.
func admitHit(policy nativePolicy, options vectorstore.SearchOptions, record nativeRecord, raw float64) (rankedDocument, bool, error) {
	matches, err := matchesMetadata(options.Filter, record.doc)
	if err != nil {
		return rankedDocument{}, false, err
	}
	if !matches {
		return rankedDocument{}, false, errors.New("milvus: selected metadata changed before search")
	}
	score, err := policy.score(raw)
	if err != nil {
		return rankedDocument{}, false, err
	}
	return rankedDocument{result: &vectorstore.SearchResult{Document: record.doc, Score: score}, raw: raw}, score >= options.MinScore, nil
}

func (s *Store) DeleteIDs(ctx context.Context, ids []string) error {
	unique, err := vectorstore.DistinctIDs(ids)
	if err != nil || len(unique) == 0 {
		return err
	}
	policy, err := s.readPolicy(ctx)
	if err != nil {
		return err
	}
	for _, id := range unique {
		if len(id) > policy.idBytes {
			return ErrDocumentIDTooLong
		}
	}
	for start := 0; start < len(unique); start += identityGroupSize {
		group := unique[start:min(start+identityGroupSize, len(unique))]
		result, err := s.client.Delete(ctx, nativeDeletion{collection: s.collectionName, ids: group})
		if err != nil {
			return err
		}
		if result.DeleteCount < 0 || result.DeleteCount > int64(len(group)) {
			return errors.New("milvus: identity deletion acknowledgment is inconsistent")
		}
	}
	return nil
}
