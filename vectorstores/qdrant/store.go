package qdrant

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	qdrantclient "github.com/qdrant/go-client/qdrant"

	"github.com/Tangerg/scope/core/embeddingclient"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

var (
	_ vectorstore.Indexer       = (*Store)(nil)
	_ vectorstore.Searcher      = (*Store)(nil)
	_ vectorstore.FilterDeleter = (*Store)(nil)
	_ vectorstore.IDDeleter     = (*Store)(nil)
)

type Store struct {
	client          APIClient
	embeddingClient embeddingclient.Client
	documentBatcher vectorstore.Batcher
	collectionName  string
	schema          nativeSchema
}

func NewStore(ctx context.Context, config StoreConfig) (*Store, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	model, err := embeddingclient.New(config.EmbeddingModel)
	if err != nil {
		return nil, err
	}
	collections, err := config.Client.ListCollections(ctx)
	if err != nil {
		return nil, err
	}
	if !slices.Contains(collections, config.CollectionName) {
		return nil, fmt.Errorf("%w: concrete collection is missing; aliases are not accepted", ErrIncompatibleCollection)
	}
	info, err := config.Client.GetCollectionInfo(ctx, config.CollectionName)
	if err != nil {
		return nil, err
	}
	var schema nativeSchema
	if err = schema.read(info); err != nil {
		return nil, err
	}
	store := &Store{client: config.Client, embeddingClient: model, documentBatcher: config.DocumentBatcher, collectionName: config.CollectionName, schema: schema}
	if _, err = store.selectMetadata(ctx, nil); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *Store) Index(ctx context.Context, request *vectorstore.IndexRequest) error {
	if err := request.Validate(); err != nil {
		return err
	}
	records := make(map[string]*qdrantclient.PointStruct, len(request.Documents))
	for _, doc := range request.Documents {
		point, err := encodeRecord(doc)
		if err != nil {
			return err
		}
		records[doc.ID] = point
	}
	batches, batchErr := request.Batch(ctx, s.documentBatcher)
	if batchErr != nil {
		return batchErr
	}
	prepared := &qdrantclient.UpsertPoints{CollectionName: s.collectionName, Wait: new(true)}
	for _, batch := range batches {
		texts, textErr := batch.Texts()
		if textErr != nil {
			return textErr
		}
		vectors, err := s.embeddingClient.EmbedTexts(ctx, texts)
		if err != nil {
			return err
		}
		for i, doc := range batch.Documents {
			vector, err := s.schema.vector(vectors[i])
			if err != nil {
				return err
			}
			point := records[doc.ID]
			point.Vectors = qdrantclient.NewVectorsDense(vector)
			prepared.Points = append(prepared.Points, point)
		}
	}
	result, err := s.client.Upsert(ctx, prepared)
	if err != nil {
		return err
	}
	return requireAppliedUpdate(result, "upsert")
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
	selected, err := s.selectMetadata(ctx, request.Options.Filter)
	if err != nil {
		return nil, err
	}
	if len(selected) == 0 {
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
		groups = slices.Collect(slices.Chunk(selected, filterPageSize))
	}
	var ranked []scoredDocument
	seen := make(map[string]struct{})
	for _, group := range groups {
		native := &qdrantclient.QueryPoints{CollectionName: s.collectionName, Limit: new(uint64(request.Options.ResultLimit())), WithPayload: qdrantclient.NewWithPayload(true), WithVectors: qdrantclient.NewWithVectors(true), Query: qdrantclient.NewQueryDense(query)}
		if group != nil {
			native.Filter = metadataSelection(group)
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
			doc, payload, err := s.schema.decode(hit.Id, hit.Payload, hit.Vectors)
			if err != nil {
				return nil, err
			}
			if _, duplicate := seen[doc.ID]; duplicate {
				return nil, errors.New("qdrant: native query repeated an identity")
			}
			seen[doc.ID] = struct{}{}
			if group != nil {
				if !slices.Contains(group, payload) {
					return nil, errors.New("qdrant: hit is outside selected metadata values")
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
					return nil, errors.New("qdrant: native hit changed Core membership")
				}
			}
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

func (s *Store) selectMetadata(ctx context.Context, predicate filter.Predicate) ([]string, error) {
	request := &qdrantclient.ScrollPoints{CollectionName: s.collectionName, Limit: new(uint32(filterPageSize)), WithPayload: qdrantclient.NewWithPayload(true), WithVectors: qdrantclient.NewWithVectors(true)}
	seen, offsets := make(map[string]struct{}), make(map[string]struct{})
	selected := make(map[string]struct{})
	for {
		if err := context.Cause(ctx); err != nil {
			return nil, err
		}
		points, next, err := s.client.ScrollAndOffset(ctx, request)
		if err != nil {
			return nil, err
		}
		if len(points) > filterPageSize {
			return nil, errors.New("qdrant: native scroll exceeded its page limit")
		}
		for _, point := range points {
			if point == nil {
				return nil, errors.New("qdrant: native scroll returned a nil point")
			}
			doc, payload, sourceErr := s.schema.decode(point.Id, point.Payload, point.Vectors)
			if sourceErr != nil {
				return nil, sourceErr
			}
			if point.ShardKey != nil {
				return nil, errors.New("qdrant: native record uses unsupported custom sharding")
			}
			if _, duplicate := seen[doc.ID]; duplicate {
				return nil, errors.New("qdrant: native scroll repeated an identity")
			}
			seen[doc.ID] = struct{}{}
			matched := true
			if predicate != nil {
				values, decodeErr := doc.Metadata.Values()
				if decodeErr != nil {
					return nil, decodeErr
				}
				matched, sourceErr = filter.Match(predicate, values)
				if sourceErr != nil {
					return nil, sourceErr
				}
			}
			if matched {
				selected[payload] = struct{}{}
			}
		}
		if next == nil {
			return slices.Sorted(maps.Keys(selected)), nil
		}
		offset, err := formatPointID(next)
		if err != nil {
			return nil, err
		}
		if _, repeated := offsets[offset]; repeated {
			return nil, errors.New("qdrant: native scroll repeated an offset")
		}
		offsets[offset] = struct{}{}
		request.Offset = next
	}
}

func (s *Store) DeleteWhere(ctx context.Context, predicate filter.Predicate) error {
	if predicate == nil {
		return vectorstore.ErrMissingFilter
	}
	if err := predicate.Validate(); err != nil {
		return err
	}
	values, err := s.selectMetadata(ctx, predicate)
	if err != nil {
		return err
	}
	for group := range slices.Chunk(values, filterPageSize) {
		result, err := s.client.Delete(ctx, s.buildDeletePoints(qdrantclient.NewPointsSelectorFilter(metadataSelection(group))))
		if err != nil {
			return err
		}
		if err = requireAppliedUpdate(result, "conditional delete"); err != nil {
			return err
		}
	}
	return nil
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
