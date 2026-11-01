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

type indexPolicy interface {
	DescribeIndex(context.Context, string) (*pineconesdk.Index, error)
}

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
	schema  nativeSchema
	space   vectorstore.SpaceID
	control indexPolicy
	name    string
	host    string
}

func NewStore(ctx context.Context, config StoreConfig) (*Store, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	native, err := config.Client.DescribeIndex(ctx, config.IndexName)
	if err != nil {
		return nil, fmt.Errorf("pinecone: describe native index: %w", err)
	}
	if native == nil || native.Tags == nil || (*native.Tags)[SpaceTag] != string(config.Space) {
		return nil, vectorstore.ErrIncompatibleSpace
	}
	var schema nativeSchema
	if err = schema.read(native, config.IndexName); err != nil {
		return nil, err
	}
	index, err := config.Client.Index(pineconesdk.NewIndexConnParams{Host: native.Host, Namespace: config.Namespace})
	if err != nil {
		return nil, err
	}
	if index == nil {
		return nil, errors.New("pinecone: native client returned no data connection")
	}
	// Construction verifies connectivity and schema compatibility; it does not
	// audit every record. Full-source integrity is an explicit maintenance concern,
	// not an implicit cost of opening the store.
	store := &Store{index: index, schema: schema, space: config.Space, control: config.Client, name: config.IndexName, host: native.Host}
	return store, nil
}

func (s *Store) verifyIndex(ctx context.Context) error {
	native, err := s.control.DescribeIndex(ctx, s.name)
	if err != nil {
		return err
	}
	if native == nil || native.Tags == nil || (*native.Tags)[SpaceTag] != string(s.space) {
		return vectorstore.ErrIncompatibleSpace
	}
	var schema nativeSchema
	if err := schema.read(native, s.name); err != nil {
		return err
	}
	if native.Host != s.host || schema != s.schema {
		return ErrIncompatibleIndex
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
	for _, doc := range docs {
		if _, err := encodeRecord(doc); err != nil {
			return err
		}
	}
	return s.verifyIndex(ctx)
}
func (s *Store) Upsert(ctx context.Context, batch *vectorstore.Batch) error {
	if err := batch.ValidateFor(s.space); err != nil {
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
		values, err := s.schema.vector(record.Values)
		if err != nil {
			return err
		}
		point.Values = &values
		points[i] = point
	}
	if err := s.verifyIndex(ctx); err != nil {
		return err
	}
	prepared := slices.Collect(slices.Chunk(points, MaxVectorsPerUpsert))
	for batchIndex, points := range prepared {
		accepted, err := s.index.UpsertVectors(ctx, points)
		if err == nil && uint64(accepted) == uint64(len(points)) {
			continue
		}
		failure := &vectorstore.WriteError{
			Acknowledged: vectorIDs(prepared[:batchIndex]),
			Unconfirmed:  vectorIDs(prepared[batchIndex : batchIndex+1]),
			Unattempted:  vectorIDs(prepared[batchIndex+1:]),
			Err:          err,
		}
		if err == nil {
			failure.Err = fmt.Errorf("pinecone: native upsert acknowledged %d of %d vectors", accepted, len(points))
			// A malformed overcount cannot certify even an aggregate acknowledgment.
			if uint64(accepted) < uint64(len(points)) {
				failure.AcknowledgedCount = new(int(accepted))
			}
		}
		return failure
	}
	return nil
}

func vectorIDs(groups [][]*pineconesdk.Vector) []string {
	var ids []string
	for _, group := range groups {
		for _, vector := range group {
			ids = append(ids, vector.Id)
		}
	}
	return ids
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
	if opts.ResultLimit() > MaxTopK {
		return fmt.Errorf("%w: TopK exceeds %d", vectorstore.ErrInvalidOptions, MaxTopK)
	}
	if _, err := nativeFilter(opts.Filter); err != nil {
		return err
	}
	return s.verifyIndex(ctx)
}
func (s *Store) Query(ctx context.Context, request *vectorstore.Query) (response *vectorstore.SearchResponse, err error) {
	if contextErr := ctx.Err(); contextErr != nil {
		return nil, contextErr
	}
	if err = request.ValidateFor(s.space); err != nil {
		return nil, err
	}
	if err = request.Options.RequireMode(vectorstore.SearchModeSemantic); err != nil {
		return nil, err
	}
	if request.Options.ResultLimit() > MaxTopK {
		return nil, fmt.Errorf("%w: TopK exceeds native limit %d", vectorstore.ErrInvalidOptions, MaxTopK)
	}
	defer func() {
		if err == nil {
			err = request.ValidateResponse(response)
		}
		if err != nil {
			response = nil
		}
	}()
	where, err := nativeFilter(request.Options.Filter)
	if err != nil {
		return nil, err
	}
	query, err := s.schema.vector(request.Values)
	if err != nil {
		return nil, err
	}
	if err := s.verifyIndex(ctx); err != nil {
		return nil, err
	}
	var ranked []scoredDocument
	seen := make(map[string]struct{})
	{
		queryReq := &pineconesdk.QueryByVectorValuesRequest{Vector: query, TopK: uint32(request.Options.ResultLimit()), IncludeMetadata: true, IncludeValues: true}
		queryReq.MetadataFilter = where
		page, err := s.index.QueryByVectorValues(ctx, queryReq)
		if err != nil {
			return nil, err
		}
		if page == nil || page.Namespace != s.index.Namespace() || len(page.Matches) > request.Options.ResultLimit() {
			return nil, errors.New("pinecone: native query returned no valid namespace acknowledgment")
		}
		for _, hit := range page.Matches {
			if hit == nil {
				return nil, errors.New("pinecone: native query omitted a scored record")
			}
			doc, _, err := s.schema.decode(hit.Vector)
			if err != nil {
				return nil, err
			}
			if _, duplicate := seen[doc.ID]; duplicate {
				return nil, errors.New("pinecone: native query repeated an identity")
			}
			seen[doc.ID] = struct{}{}

			raw := float64(hit.Score)
			score, err := s.schema.score(raw)
			if err != nil {
				return nil, err
			}
			result, err := vectorstore.NewSearchResult(doc, score)
			if err != nil {
				return nil, err
			}
			rank := raw
			if s.schema.metric == pineconesdk.IndexMetricEuclidean {
				rank = -raw
			}
			ranked = append(ranked, scoredDocument{result: result, rank: rank})
		}
	}
	slices.SortFunc(ranked, func(left, right scoredDocument) int {
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
	if err := s.verifyIndex(ctx); err != nil {
		return err
	}
	return s.index.DeleteVectorsByFilter(ctx, where)
}

func (s *Store) DeleteIDs(ctx context.Context, ids []string) error {
	seen := make(map[string]struct{})
	var unique []string
	for _, id := range ids {
		if err := validateNativeIdentifier(id, false); err != nil {
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
	if err := s.verifyIndex(ctx); err != nil {
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
