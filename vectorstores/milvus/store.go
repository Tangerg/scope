package milvus

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/milvus-io/milvus/client/v2/column"
	"github.com/milvus-io/milvus/client/v2/entity"
	"github.com/milvus-io/milvus/client/v2/milvusclient"
	"google.golang.org/grpc"

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
	client          collectionClient
	embeddingClient embeddingclient.Client
	documentBatcher vectorstore.Batcher
	collectionName  string
}

func NewStore(ctx context.Context, config StoreConfig) (*Store, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	embeddings, err := embeddingclient.New(config.EmbeddingModel)
	if err != nil {
		return nil, err
	}
	s := &Store{client: config.Client, embeddingClient: embeddings, documentBatcher: config.DocumentBatcher, collectionName: config.CollectionName}
	policy, err := s.readPolicy(ctx)
	if err != nil {
		return nil, err
	}
	if _, err = s.selectDocuments(ctx, policy, nil); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) readPolicy(ctx context.Context) (nativePolicy, error) {
	collection, err := s.client.DescribeCollection(ctx, milvusclient.NewDescribeCollectionOption(s.collectionName))
	if err != nil {
		return nativePolicy{}, err
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

func (s *Store) selectDocuments(ctx context.Context, policy nativePolicy, predicate filter.Predicate) ([]nativeRecord, error) {
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

func (s *Store) Index(ctx context.Context, request *vectorstore.IndexRequest) error {
	if err := request.Validate(); err != nil {
		return err
	}
	for _, doc := range request.Documents {
		if doc.Media != nil {
			return fmt.Errorf("%w: media is unsupported", vectorstore.ErrInvalidDocument)
		}
	}
	policy, err := s.readPolicy(ctx)
	if err != nil {
		return err
	}
	records, err := encodeDocuments(request.Documents, policy)
	if err != nil {
		return err
	}
	batches, err := request.Batch(ctx, s.documentBatcher)
	if err != nil {
		return err
	}
	prepared := make([][]column.Column, 0, len(batches))
	position := 0
	for _, batch := range batches {
		texts, textErr := batch.Texts()
		if textErr != nil {
			return textErr
		}
		vectors, embedErr := s.embeddingClient.EmbedTexts(ctx, texts)
		if embedErr != nil {
			return embedErr
		}
		columns, columnErr := encodeColumns(records[position:position+len(batch.Documents)], vectors, policy)
		if columnErr != nil {
			return columnErr
		}
		prepared = append(prepared, columns)
		position += len(batch.Documents)
	}
	if _, err = s.selectDocuments(ctx, policy, nil); err != nil {
		return err
	}
	for _, columns := range prepared {
		result, err := s.client.Upsert(ctx, milvusclient.NewColumnBasedInsertOption(s.collectionName, columns...))
		if err != nil {
			return err
		}
		count := columns[0].Len()
		if result.UpsertCount != int64(count) || result.IDs == nil || result.IDs.Len() != count {
			return fmt.Errorf("milvus: upsert acknowledgment does not cover %d documents", count)
		}
		for position := range count {
			id, err := result.IDs.GetAsString(position)
			if err != nil {
				return err
			}
			expected, err := columns[0].GetAsString(position)
			if err != nil {
				return err
			}
			if id != expected {
				return errors.New("milvus: upsert acknowledged a different identity")
			}
		}
	}
	return nil
}

func (s *Store) Search(ctx context.Context, request *vectorstore.SearchRequest) (*vectorstore.SearchResponse, error) {
	if err := request.Validate(); err != nil {
		return nil, err
	}
	if err := request.Options.RequireMode(vectorstore.SearchModeSemantic); err != nil {
		return nil, err
	}
	limit := request.Options.ResultLimit()
	if limit > nativeMaxTopK {
		return nil, errors.New("milvus: result limit exceeds the native topK limit")
	}
	policy, err := s.readPolicy(ctx)
	if err != nil {
		return nil, err
	}
	candidates, err := s.selectDocuments(ctx, policy, request.Options.Filter)
	if err != nil {
		return nil, err
	}
	response := &vectorstore.SearchResponse{}
	if len(candidates) == 0 {
		return response, response.ValidateFor(request)
	}
	vector, err := s.embeddingClient.EmbedText(ctx, request.Query)
	if err != nil {
		return nil, err
	}
	query, err := policy.vector(vector)
	if err != nil {
		return nil, err
	}
	var ranked []rankedDocument
	for start := 0; start < len(candidates); start += identityGroupSize {
		group := candidates[start:min(start+identityGroupSize, len(candidates))]
		ids := make([]string, len(group))
		for position, record := range group {
			ids[position] = record.doc.ID
		}
		options := milvusclient.NewSearchOption(s.collectionName, min(limit, len(group)), []entity.Vector{entity.FloatVector(query)}).
			WithANNSField(fieldVector).WithOutputFields(fieldID, fieldContent, fieldMeta, fieldVector).
			WithFilter("id in {ids}").WithTemplateParam("ids", ids).WithConsistencyLevel(entity.ClStrong).
			WithSearchParam("metric_type", string(policy.metric))
		results, err := s.client.Search(ctx, options)
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
		if len(rows.Scores) != len(records) || len(records) > min(limit, len(group)) || len(records) != 0 && rows.IDs == nil || rows.IDs != nil && rows.IDs.Len() != len(records) {
			return nil, errors.New("milvus: search result shape is inconsistent")
		}
		seen := make(map[string]bool, len(records))
		for position, record := range records {
			nativeID, err := rows.IDs.GetAsString(position)
			if err != nil {
				return nil, err
			}
			if nativeID != record.doc.ID || !slices.Contains(ids, record.doc.ID) || seen[record.doc.ID] {
				return nil, errors.New("milvus: search identity is inconsistent")
			}
			seen[record.doc.ID] = true
			matches, err := matchesMetadata(request.Options.Filter, record.doc)
			if err != nil {
				return nil, err
			}
			if !matches {
				return nil, errors.New("milvus: selected metadata changed before search")
			}
			raw := float64(rows.Scores[position])
			score, err := policy.score(raw)
			if err != nil {
				return nil, err
			}
			if score < request.Options.MinScore {
				continue
			}
			ranked = append(ranked, rankedDocument{result: &vectorstore.SearchResult{Document: record.doc, Score: score}, raw: raw})
		}
	}
	slices.SortFunc(ranked, func(left, right rankedDocument) int {
		order := cmp.Compare(right.raw, left.raw)
		if policy.metric == entity.L2 {
			order = -order
		}
		if order != 0 {
			return order
		}
		return cmp.Compare(left.result.Document.ID, right.result.Document.ID)
	})
	for _, hit := range ranked[:min(limit, len(ranked))] {
		response.Results = append(response.Results, hit.result)
	}
	if err := response.ValidateFor(request); err != nil {
		return nil, err
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
	policy, err := s.readPolicy(ctx)
	if err != nil {
		return err
	}
	records, err := s.selectDocuments(ctx, policy, predicate)
	if err != nil {
		return err
	}
	for _, record := range records {
		result, err := s.client.Delete(ctx, nativeDeletion{collection: s.collectionName, ids: []string{record.doc.ID}, metadata: &record.metadata})
		if err != nil {
			return err
		}
		if result.DeleteCount < 0 || result.DeleteCount > 1 {
			return errors.New("milvus: conditional deletion acknowledgment is inconsistent")
		}
	}
	return nil
}

func (s *Store) DeleteIDs(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	for _, id := range ids {
		if strings.TrimSpace(id) == "" || !utf8.ValidString(id) {
			return vectorstore.ErrInvalidDocument
		}
	}
	policy, err := s.readPolicy(ctx)
	if err != nil {
		return err
	}
	unique := make([]string, 0, len(ids))
	for _, id := range ids {
		if len(id) > policy.idBytes {
			return ErrDocumentIDTooLong
		}
		if !slices.Contains(unique, id) {
			unique = append(unique, id)
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
