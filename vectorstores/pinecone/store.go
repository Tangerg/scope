package pinecone

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	pineconesdk "github.com/pinecone-io/go-pinecone/v4/pinecone"

	"github.com/Tangerg/scope/core/embeddingclient"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

var (
	_ vectorstore.Indexer       = (*Store)(nil)
	_ vectorstore.Searcher      = (*Store)(nil)
	_ vectorstore.IDDeleter     = (*Store)(nil)
	_ vectorstore.FilterDeleter = (*Store)(nil)
	_ vectorstore.Closer        = (*Store)(nil)
)

type indexConnection interface {
	UpsertVectors(context.Context, []*pineconesdk.Vector) (uint32, error)
	QueryByVectorValues(context.Context, *pineconesdk.QueryByVectorValuesRequest) (*pineconesdk.QueryVectorsResponse, error)
	ListVectors(context.Context, *pineconesdk.ListVectorsRequest) (*pineconesdk.ListVectorsResponse, error)
	FetchVectors(context.Context, []string) (*pineconesdk.FetchVectorsResponse, error)
	DeleteVectorsById(context.Context, []string) error
	DeleteVectorsByFilter(context.Context, *pineconesdk.MetadataFilter) error
	Namespace() string
	Close() error
}

type Store struct {
	index           indexConnection
	schema          nativeSchema
	embeddingClient embeddingclient.Client
	documentBatcher vectorstore.Batcher
}

func NewStore(ctx context.Context, config StoreConfig) (*Store, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	model, err := embeddingclient.New(config.EmbeddingModel)
	if err != nil {
		return nil, err
	}
	native, err := config.Client.DescribeIndex(ctx, config.IndexName)
	if err != nil {
		return nil, fmt.Errorf("pinecone: describe native index: %w", err)
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
	store := &Store{index: index, schema: schema, embeddingClient: model, documentBatcher: config.DocumentBatcher}
	if _, err = store.selectMetadata(ctx, nil); err != nil {
		return nil, errors.Join(err, index.Close())
	}
	return store, nil
}

func (s *Store) Index(ctx context.Context, request *vectorstore.IndexRequest) error {
	if err := request.Validate(); err != nil {
		return err
	}
	records := make(map[string]*pineconesdk.Vector, len(request.Documents))
	for _, doc := range request.Documents {
		record, err := encodeRecord(doc)
		if err != nil {
			return err
		}
		records[doc.ID] = record
	}
	batches, err := request.Batch(ctx, s.documentBatcher)
	if err != nil {
		return err
	}
	var prepared [][]*pineconesdk.Vector
	for _, batch := range batches {
		texts, err := batch.Texts()
		if err != nil {
			return err
		}
		vectors, err := s.embeddingClient.EmbedTexts(ctx, texts)
		if err != nil {
			return err
		}
		points := make([]*pineconesdk.Vector, len(batch.Documents))
		for i, doc := range batch.Documents {
			vector, err := s.schema.vector(vectors[i])
			if err != nil {
				return err
			}
			point := records[doc.ID]
			point.Values = &vector
			points[i] = point
		}
		prepared = append(prepared, slices.Collect(slices.Chunk(points, MaxVectorsPerUpsert))...)
	}
	for _, points := range prepared {
		accepted, err := s.index.UpsertVectors(ctx, points)
		if err != nil {
			return err
		}
		if uint64(accepted) != uint64(len(points)) {
			return fmt.Errorf("pinecone: native upsert acknowledged %d of %d vectors", accepted, len(points))
		}
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
	if request.Options.ResultLimit() > MaxTopK {
		return nil, fmt.Errorf("%w: TopK exceeds native limit %d", vectorstore.ErrInvalidOptions, MaxTopK)
	}
	defer func() {
		if err == nil {
			err = response.ValidateFor(request)
		}
		if err != nil {
			response = nil
		}
	}()
	values, err := s.selectMetadata(ctx, request.Options.Filter)
	if err != nil {
		return nil, err
	}
	if len(values) == 0 {
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
		groups = slices.Collect(slices.Chunk(values, filterGroupSize))
	}
	var ranked []scoredDocument
	seen := make(map[string]struct{})
	for _, group := range groups {
		queryReq := &pineconesdk.QueryByVectorValuesRequest{Vector: query, TopK: uint32(request.Options.ResultLimit()), IncludeMetadata: true, IncludeValues: true}
		if group != nil {
			queryReq.MetadataFilter = metadataSelection(group)
		}
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
			doc, payload, err := s.schema.decode(hit.Vector)
			if err != nil {
				return nil, err
			}
			if _, duplicate := seen[doc.ID]; duplicate {
				return nil, errors.New("pinecone: native query repeated an identity")
			}
			seen[doc.ID] = struct{}{}
			if group != nil {
				if !slices.Contains(group, payload) {
					return nil, errors.New("pinecone: native hit is outside selected metadata values")
				}
				facts, decodeErr := doc.Metadata.Values()
				if decodeErr != nil {
					return nil, decodeErr
				}
				matched, matchErr := filter.Match(request.Options.Filter, facts)
				if matchErr != nil {
					return nil, matchErr
				}
				if !matched {
					return nil, errors.New("pinecone: native hit changed Core membership")
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
			if s.schema.metric == pineconesdk.Euclidean {
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
	var token *string
	tokens := make(map[string]struct{})
	idsSeen := make(map[string]struct{})
	selected := make(map[string]struct{})
	for {
		page, err := s.index.ListVectors(ctx, &pineconesdk.ListVectorsRequest{Limit: new(metadataListPageSize), PaginationToken: token})
		if err != nil {
			return nil, err
		}
		if page == nil || page.Namespace != s.index.Namespace() || len(page.VectorIds) > int(metadataListPageSize) {
			return nil, errors.New("pinecone: native list omitted the namespace acknowledgment")
		}
		var ids []string
		for _, id := range page.VectorIds {
			if id == nil || *id == "" {
				return nil, errors.New("pinecone: native list omitted an ID")
			}
			if _, duplicate := idsSeen[*id]; duplicate {
				return nil, errors.New("pinecone: native list repeated an ID")
			}
			idsSeen[*id] = struct{}{}
			ids = append(ids, *id)
		}
		if len(ids) > 0 {
			fetched, err := s.index.FetchVectors(ctx, ids)
			if err != nil {
				return nil, err
			}
			if fetched == nil || fetched.Namespace != s.index.Namespace() || len(fetched.Vectors) != len(ids) {
				return nil, errors.New("pinecone: native fetch did not acknowledge every selected ID")
			}
			for _, id := range ids {
				point := fetched.Vectors[id]
				if point == nil || point.Id != id {
					return nil, errors.New("pinecone: native fetch changed a selected identity")
				}
				doc, payload, err := s.schema.decode(point)
				if err != nil {
					return nil, err
				}
				matched := true
				if predicate != nil {
					facts, decodeErr := doc.Metadata.Values()
					if decodeErr != nil {
						return nil, decodeErr
					}
					matched, err = filter.Match(predicate, facts)
					if err != nil {
						return nil, err
					}
				}
				if matched {
					selected[payload] = struct{}{}
				}
			}
		}
		if page.NextPaginationToken == nil || *page.NextPaginationToken == "" {
			return slices.Sorted(maps.Keys(selected)), nil
		}
		next := *page.NextPaginationToken
		if _, duplicate := tokens[next]; duplicate {
			return nil, errors.New("pinecone: native list repeated a pagination token")
		}
		tokens[next] = struct{}{}
		token = new(next)
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
	for group := range slices.Chunk(values, filterGroupSize) {
		if err := s.index.DeleteVectorsByFilter(ctx, metadataSelection(group)); err != nil {
			return err
		}
	}
	return nil
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
	for group := range slices.Chunk(unique, maximumIDsPerDelete) {
		if err := s.index.DeleteVectorsById(ctx, group); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) Close() error { return s.index.Close() }
