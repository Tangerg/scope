package qdrant

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	qdrantclient "github.com/qdrant/go-client/qdrant"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

var (
	_ vectorstore.Upserter      = (*Store)(nil)
	_ vectorstore.Querier       = (*Store)(nil)
	_ vectorstore.FilterDeleter = (*Store)(nil)
	_ vectorstore.IDDeleter     = (*Store)(nil)
)

type Store struct {
	client         APIClient
	space          vectorstore.SpaceID
	collectionName string
	schema         nativeSchema
}

func NewStore(ctx context.Context, config StoreConfig) (*Store, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	collections, err := config.Client.ListCollections(ctx)
	if err != nil {
		return nil, err
	}
	if !slices.Contains(collections, config.CollectionName) {
		return nil, fmt.Errorf("%w: concrete collection is missing; aliases are not accepted", ErrIncompatibleCollection)
	}
	store := &Store{client: config.Client, collectionName: config.CollectionName, space: config.Space}
	store.schema, err = store.readSchema(ctx)
	if err != nil {
		return nil, err
	}
	return store, nil
}

func (s *Store) readSchema(ctx context.Context) (nativeSchema, error) {
	info, err := s.client.GetCollectionInfo(ctx, s.collectionName)
	if err != nil {
		return nativeSchema{}, err
	}
	if info.GetConfig().GetMetadata()[SpaceMetadataKey].GetStringValue() != string(s.space) {
		return nativeSchema{}, fmt.Errorf("%w: collection does not declare %q", vectorstore.ErrIncompatibleSpace, s.space)
	}
	var schema nativeSchema
	err = schema.read(info)
	return schema, err
}
func (s *Store) verifyCollection(ctx context.Context) error {
	schema, err := s.readSchema(ctx)
	if err != nil {
		return err
	}
	if schema != s.schema {
		return fmt.Errorf("%w: collection vector configuration changed", ErrIncompatibleCollection)
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
		if _, err := encodeRecord(d); err != nil {
			return err
		}
	}
	return s.verifyCollection(ctx)
}

func (s *Store) Upsert(ctx context.Context, batch *vectorstore.Batch) error {
	if err := batch.ValidateFor(s.space); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	req := &qdrantclient.UpsertPoints{CollectionName: s.collectionName, Wait: new(true)}
	for _, record := range batch.Records {
		point, err := encodeRecord(record.Document)
		if err != nil {
			return err
		}
		values, err := s.schema.vector(record.Values)
		if err != nil {
			return err
		}
		point.Vectors = qdrantclient.NewVectorsDense(values)
		req.Points = append(req.Points, point)
	}
	if err := s.verifyCollection(ctx); err != nil {
		return err
	}
	res, err := s.client.Upsert(ctx, req)
	if err == nil {
		err = requireAppliedUpdate(res, "upsert")
	}
	if err != nil {
		ids := make([]string, len(batch.Records))
		for i, r := range batch.Records {
			ids[i] = r.Document.ID
		}
		if res.GetStatus() == qdrantclient.UpdateStatus_ClockRejected {
			return &vectorstore.WriteError{Err: err, Rejected: ids}
		}
		return &vectorstore.WriteError{Err: err, Unconfirmed: ids}
	}
	return nil
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
	if _, err := nativeFilter(opts.Filter); err != nil {
		return err
	}
	return s.verifyCollection(ctx)
}

func (s *Store) Query(ctx context.Context, request *vectorstore.Query) (response *vectorstore.SearchResponse, err error) {
	if err = request.ValidateFor(s.space); err != nil {
		return nil, err
	}
	if err = request.Options.RequireMode(vectorstore.SearchModeSemantic); err != nil {
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
	where, err := nativeFilter(request.Options.Filter)
	if err != nil {
		return nil, err
	}
	query, err := s.schema.vector(request.Values)
	if err != nil {
		return nil, err
	}
	var ranked []scoredDocument
	seen := make(map[string]struct{})
	{
		native := &qdrantclient.QueryPoints{CollectionName: s.collectionName, Limit: new(uint64(request.Options.ResultLimit())), WithPayload: qdrantclient.NewWithPayload(true), WithVectors: qdrantclient.NewWithVectors(true), Query: qdrantclient.NewQueryDense(query)}
		native.Filter = where
		if err := s.verifyCollection(ctx); err != nil {
			return nil, err
		}
		hits, err := s.client.Query(ctx, native)
		if err != nil {
			return nil, err
		}
		if len(hits) > request.Options.ResultLimit() {
			return nil, errors.New("qdrant: native query exceeded its result limit")
		}
		for _, hit := range hits {
			if hit == nil || hit.ShardKey != nil {
				return nil, errors.New("qdrant: native query returned a missing or shard-keyed hit")
			}
			doc, _, err := s.schema.decode(hit.Id, hit.Payload, hit.Vectors)
			if err != nil {
				return nil, err
			}
			if _, duplicate := seen[doc.ID]; duplicate {
				return nil, errors.New("qdrant: native query repeated an identity")
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
			if s.schema.metric == qdrantclient.Distance_Euclid || s.schema.metric == qdrantclient.Distance_Manhattan {
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
	if err = s.verifyCollection(ctx); err != nil {
		return err
	}
	result, err := s.client.Delete(ctx, s.buildDeletePoints(qdrantclient.NewPointsSelectorFilter(where)))
	if err != nil {
		return err
	}
	return requireAppliedUpdate(result, "conditional delete")
}

func (s *Store) DeleteIDs(ctx context.Context, ids []string) error {
	seen := make(map[string]struct{})
	var points []*qdrantclient.PointId
	for _, id := range ids {
		point, err := parsePointID(id)
		if err != nil {
			return err
		}
		if _, duplicate := seen[id]; !duplicate {
			seen[id] = struct{}{}
			points = append(points, point)
		}
	}
	if len(points) == 0 {
		return nil
	}
	if err := s.verifyCollection(ctx); err != nil {
		return err
	}
	result, err := s.client.Delete(ctx, s.buildDeletePoints(qdrantclient.NewPointsSelector(points...)))
	if err != nil {
		return err
	}
	return requireAppliedUpdate(result, "delete by ID")
}

func (s *Store) buildDeletePoints(selector *qdrantclient.PointsSelector) *qdrantclient.DeletePoints {
	return &qdrantclient.DeletePoints{CollectionName: s.collectionName, Wait: new(true), Points: selector}
}

func requireAppliedUpdate(result *qdrantclient.UpdateResult, operation string) error {
	if result == nil || result.Status != qdrantclient.UpdateStatus_Completed {
		return fmt.Errorf("qdrant: %s did not complete: %s", operation, result.GetStatus())
	}
	return nil
}
