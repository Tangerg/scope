package pinecone

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	pineconesdk "github.com/pinecone-io/go-pinecone/v7/pinecone"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

var (
	_ vectorstore.Upserter      = (*Store)(nil)
	_ vectorstore.Querier       = (*Store)(nil)
	_ vectorstore.IDDeleter     = (*Store)(nil)
	_ vectorstore.FilterDeleter = (*Store)(nil)
	_ vectorstore.Closer        = (*Store)(nil)
)

type indexConnection interface {
	UpsertVectors(context.Context, []*pineconesdk.Vector) (uint32, error)
	QueryByVectorValues(context.Context, *pineconesdk.QueryByVectorValuesRequest) (*pineconesdk.QueryVectorsResponse, error)
	DeleteVectorsById(context.Context, []string) error
	DeleteVectorsByFilter(context.Context, *pineconesdk.MetadataFilter) error
	Namespace() string
	Close() error
}

type Store struct {
	index   indexConnection
	binding indexBinding
}

func NewStore(ctx context.Context, config StoreConfig) (*Store, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	binding, err := readIndexBinding(ctx, config.Client, config.IndexName, config.Space)
	if err != nil {
		return nil, err
	}
	index, err := config.Client.Index(pineconesdk.NewIndexConnParams{Host: binding.host, Namespace: config.Namespace})
	if err != nil {
		return nil, err
	}
	if index == nil {
		return nil, errors.New("pinecone: native client returned no data connection")
	}
	// Construction verifies connectivity and schema compatibility; it does not
	// audit every record. Full-source integrity is an explicit maintenance concern,
	// not an implicit cost of opening the store.
	store := &Store{index: index, binding: binding}
	return store, nil
}

func (s *Store) CheckDocuments(ctx context.Context, space vectorstore.SpaceID, docs []*document.Document) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := space.Require(s.binding.space); err != nil {
		return err
	}
	if err := vectorstore.ValidateDocuments(docs); err != nil {
		return err
	}
	for _, doc := range docs {
		if _, err := encodeRecord(doc); err != nil {
			return err
		}
	}
	return s.binding.verify(ctx)
}

func (s *Store) Upsert(ctx context.Context, batch *vectorstore.Batch) error {
	if err := batch.ValidateFor(s.binding.space); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	points := make([]*pineconesdk.Vector, len(batch.Records))
	for i, record := range batch.Records {
		point, err := encodeRecord(record.Document)
		if err != nil {
			return err
		}
		values, err := s.binding.schema.vector(record.Values)
		if err != nil {
			return err
		}
		point.Values = &values
		points[i] = point
	}
	prepared, err := newUpsertBatchPlan(s.index.Namespace(), points)
	if err != nil {
		return err
	}
	if err := s.binding.verify(ctx); err != nil {
		return err
	}
	for batchIndex, points := range prepared {
		accepted, err := s.index.UpsertVectors(ctx, points)
		if err == nil && uint64(accepted) == uint64(len(points)) {
			continue
		}
		outcome := vectorstore.WriteOutcome{
			Acknowledged: prepared[:batchIndex].ids(),
			Unconfirmed:  prepared[batchIndex : batchIndex+1].ids(),
			Unattempted:  prepared[batchIndex+1:].ids(),
		}
		if err == nil {
			err = fmt.Errorf("pinecone: native upsert acknowledged %d of %d vectors", accepted, len(points))
			// A malformed overcount cannot certify even an aggregate acknowledgment.
			if uint64(accepted) < uint64(len(points)) {
				outcome.AcknowledgedCount = new(int(accepted))
			}
		}
		return vectorstore.NewWriteError(err, outcome)
	}
	return nil
}

func (s *Store) CheckQuery(ctx context.Context, space vectorstore.SpaceID, opts vectorstore.SearchOptions) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := s.binding.admitQuery(space, opts); err != nil {
		return err
	}
	return s.binding.verify(ctx)
}

func (s *Store) Query(ctx context.Context, request *vectorstore.Query) (response *vectorstore.SearchResponse, err error) {
	if contextErr := ctx.Err(); contextErr != nil {
		return nil, contextErr
	}
	if err = request.ValidateFor(s.binding.space); err != nil {
		return nil, err
	}
	where, err := s.binding.admitQuery(request.Space, request.Options)
	if err != nil {
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
	query, err := s.binding.schema.vector(request.Values)
	if err != nil {
		return nil, err
	}
	if err = s.binding.verify(ctx); err != nil {
		return nil, err
	}
	native := &pineconesdk.QueryByVectorValuesRequest{Vector: query, TopK: uint32(request.Options.ResultLimit()), IncludeMetadata: true, IncludeValues: true, MetadataFilter: where}
	page, err := s.index.QueryByVectorValues(ctx, native)
	if err != nil {
		return nil, err
	}
	if page == nil || page.Namespace != s.index.Namespace() {
		return nil, errors.New("pinecone: native query returned no valid namespace acknowledgment")
	}
	ranked, err := s.rankHits(page.Matches, request.Options.ResultLimit())
	if err != nil {
		return nil, err
	}
	response = &vectorstore.SearchResponse{}
	for _, hit := range ranked {
		if hit.result.Score >= request.Options.MinScore {
			response.Results = append(response.Results, hit.result)
		}
	}
	return response, nil
}

// rankHits decodes native matches nearest first and rejects what a query of
// one namespace can never return.
func (s *Store) rankHits(hits []*pineconesdk.ScoredVector, limit int) ([]scoredDocument, error) {
	if len(hits) > limit {
		return nil, errors.New("pinecone: native query exceeded its result limit")
	}
	ranked := make([]scoredDocument, 0, len(hits))
	seen := make(map[string]struct{}, len(hits))
	for _, hit := range hits {
		if hit == nil {
			return nil, errors.New("pinecone: native query omitted a scored record")
		}
		doc, _, err := s.binding.schema.decode(hit.Vector)
		if err != nil {
			return nil, err
		}
		if _, duplicate := seen[doc.ID]; duplicate {
			return nil, errors.New("pinecone: native query repeated an identity")
		}
		seen[doc.ID] = struct{}{}
		raw := float64(hit.Score)
		score, err := s.binding.schema.score(raw)
		if err != nil {
			return nil, err
		}
		result, err := vectorstore.NewSearchResult(doc, score)
		if err != nil {
			return nil, err
		}
		ranked = append(ranked, scoredDocument{result: result, rank: s.binding.schema.nearness(raw)})
	}
	slices.SortFunc(ranked, func(left, right scoredDocument) int {
		return cmp.Or(cmp.Compare(right.rank, left.rank), strings.Compare(left.result.Document.ID, right.result.Document.ID))
	})
	return ranked[:min(len(ranked), limit)], nil
}

func (s *Store) DeleteWhere(ctx context.Context, predicate filter.Predicate) error {
	if predicate == nil {
		return vectorstore.ErrMissingFilter
	}
	if err := predicate.Validate(); err != nil {
		return err
	}
	where, err := nativeFilter(predicate)
	if err != nil {
		return err
	}
	if err := s.binding.verify(ctx); err != nil {
		return err
	}
	return s.index.DeleteVectorsByFilter(ctx, where)
}

func (s *Store) DeleteIDs(ctx context.Context, ids []string) error {
	unique, err := vectorstore.DistinctIDs(ids)
	if err != nil {
		return err
	}
	for _, id := range unique {
		if err := validateNativeIdentifier(id, false); err != nil {
			return err
		}
	}
	if len(unique) == 0 {
		return nil
	}
	if err := s.binding.verify(ctx); err != nil {
		return err
	}
	for group := range slices.Chunk(unique, maximumIDsPerDelete) {
		if err := s.index.DeleteVectorsById(ctx, group); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) Close() error { return s.index.Close() }
