package s3vectors

import (
	"cmp"
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"slices"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3vectors"
	s3vdoc "github.com/aws/aws-sdk-go-v2/service/s3vectors/document"
	"github.com/aws/aws-sdk-go-v2/service/s3vectors/types"
	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/embeddingclient"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

const (
	Provider               = "S3Vectors"
	MaxTopK                = 10_000
	MaxResultsPerQueryPage = 100
	MaxVectorsPerWrite     = 500
	maxRequestBytes        = 20 * 1024 * 1024
	maxQueryKeys           = 1000
)

var ErrIncompatibleIndex = errors.New("s3vectors: index is incompatible")

type VectorClient interface {
	GetIndex(context.Context, *awss3.GetIndexInput, ...func(*awss3.Options)) (*awss3.GetIndexOutput, error)
	PutVectors(context.Context, *awss3.PutVectorsInput, ...func(*awss3.Options)) (*awss3.PutVectorsOutput, error)
	QueryVectors(context.Context, *awss3.QueryVectorsInput, ...func(*awss3.Options)) (*awss3.QueryVectorsOutput, error)
	ListVectors(context.Context, *awss3.ListVectorsInput, ...func(*awss3.Options)) (*awss3.ListVectorsOutput, error)
	DeleteVectors(context.Context, *awss3.DeleteVectorsInput, ...func(*awss3.Options)) (*awss3.DeleteVectorsOutput, error)
}

type StoreConfig struct {
	Client           VectorClient
	VectorBucketName string
	IndexName        string
	EmbeddingModel   embedding.Model
	DocumentBatcher  vectorstore.Batcher
}

func (s StoreConfig) Validate() error {
	if lo.IsNil(s.Client) {
		return errors.New("s3vectors: Client is required")
	}
	if s.VectorBucketName == "" {
		return errors.New("s3vectors: VectorBucketName is required")
	}
	if s.IndexName == "" {
		return errors.New("s3vectors: IndexName is required")
	}
	if lo.IsNil(s.EmbeddingModel) {
		return errors.New("s3vectors: EmbeddingModel is required")
	}
	if lo.IsNil(s.DocumentBatcher) {
		return errors.New("s3vectors: DocumentBatcher is required")
	}
	return nil
}

var (
	_ vectorstore.Indexer   = (*Store)(nil)
	_ vectorstore.Searcher  = (*Store)(nil)
	_ vectorstore.IDDeleter = (*Store)(nil)
)

type Store struct {
	client           VectorClient
	vectorBucketName string
	indexName        string
	embeddingClient  embeddingclient.Client
	documentBatcher  vectorstore.Batcher
	schema           indexSchema
}

func NewStore(ctx context.Context, config StoreConfig) (*Store, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	client, err := embeddingclient.New(config.EmbeddingModel)
	if err != nil {
		return nil, err
	}
	output, err := config.Client.GetIndex(ctx, &awss3.GetIndexInput{VectorBucketName: aws.String(config.VectorBucketName), IndexName: aws.String(config.IndexName)})
	if err != nil {
		return nil, fmt.Errorf("s3vectors: GetIndex: %w", err)
	}
	if output == nil {
		return nil, fmt.Errorf("%w: GetIndex returned no response", ErrIncompatibleIndex)
	}
	schema, err := newIndexSchema(output.Index, config.VectorBucketName, config.IndexName)
	if err != nil {
		return nil, err
	}
	store := &Store{client: config.Client, vectorBucketName: config.VectorBucketName, indexName: config.IndexName, embeddingClient: client, documentBatcher: config.DocumentBatcher, schema: schema}
	if _, err = store.matchingKeys(ctx, nil); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *Store) Index(ctx context.Context, request *vectorstore.IndexRequest) error {
	if err := request.Validate(); err != nil {
		return err
	}
	encoded := make(map[string]s3vdoc.Interface, len(request.Documents))
	for _, doc := range request.Documents {
		metadata, err := encodeDocument(doc)
		if err != nil {
			return err
		}
		encoded[doc.ID] = metadata
	}
	batches, err := request.Batch(ctx, s.documentBatcher)
	if err != nil {
		return err
	}
	var records []types.PutInputVector
	for _, batch := range batches {
		texts, textErr := batch.Texts()
		if textErr != nil {
			return textErr
		}
		vectors, embedErr := s.embeddingClient.EmbedTexts(ctx, texts)
		if embedErr != nil {
			return embedErr
		}
		for i, doc := range batch.Documents {
			vector, vectorErr := s.schema.narrow(vectors[i])
			if vectorErr != nil {
				return fmt.Errorf("s3vectors: document %s: %w", doc.ID, vectorErr)
			}
			records = append(records, types.PutInputVector{Key: aws.String(doc.ID), Data: &types.VectorDataMemberFloat32{Value: vector}, Metadata: encoded[doc.ID]})
		}
	}
	requests, err := s.putRequests(records)
	if err != nil {
		return err
	}
	for _, input := range requests {
		output, err := s.client.PutVectors(ctx, input)
		if err != nil {
			return fmt.Errorf("s3vectors: PutVectors: %w", err)
		}
		if output == nil {
			return errors.New("s3vectors: PutVectors returned no acknowledgment")
		}
	}
	return nil
}

func (s *Store) putRequests(records []types.PutInputVector) ([]*awss3.PutVectorsInput, error) {
	base, err := jsonv2.Marshal(map[string]any{"vectorBucketName": s.vectorBucketName, "indexName": s.indexName, "vectors": []any{}})
	if err != nil {
		return nil, err
	}
	var requests []*awss3.PutVectorsInput
	var chunk []types.PutInputVector
	size := len(base)
	for _, record := range records {
		metadata, err := record.Metadata.MarshalSmithyDocument()
		if err != nil {
			return nil, err
		}
		vector := record.Data.(*types.VectorDataMemberFloat32)
		encoded, err := jsonv2.Marshal(map[string]any{"key": *record.Key, "data": map[string]any{"float32": vector.Value}, "metadata": json.RawMessage(metadata)})
		if err != nil {
			return nil, err
		}
		if len(base)+len(encoded) > maxRequestBytes {
			return nil, errors.New("s3vectors: one native vector exceeds the request budget")
		}
		if len(chunk) == MaxVectorsPerWrite || size+len(encoded)+1 > maxRequestBytes {
			requests = append(requests, &awss3.PutVectorsInput{VectorBucketName: aws.String(s.vectorBucketName), IndexName: aws.String(s.indexName), Vectors: chunk})
			chunk = nil
			size = len(base)
		}
		chunk = append(chunk, record)
		size += len(encoded) + 1
	}
	if len(chunk) != 0 {
		requests = append(requests, &awss3.PutVectorsInput{VectorBucketName: aws.String(s.vectorBucketName), IndexName: aws.String(s.indexName), Vectors: chunk})
	}
	return requests, nil
}

func (s *Store) matchingKeys(ctx context.Context, predicate filter.Predicate) ([]string, error) {
	var keys []string
	seenKeys := make(map[string]struct{})
	seenTokens := make(map[string]struct{})
	input := &awss3.ListVectorsInput{VectorBucketName: aws.String(s.vectorBucketName), IndexName: aws.String(s.indexName), MaxResults: aws.Int32(1000), ReturnMetadata: true, ReturnData: true}
	for {
		page, err := s.client.ListVectors(ctx, input)
		if err != nil {
			return nil, fmt.Errorf("s3vectors: ListVectors: %w", err)
		}
		if page == nil || page.Vectors == nil {
			return nil, errors.New("s3vectors: ListVectors returned no vector array")
		}
		for _, row := range page.Vectors {
			if row.Key == nil {
				return nil, errors.New("s3vectors: listed vector is missing its key")
			}
			if _, duplicate := seenKeys[*row.Key]; duplicate {
				return nil, errors.New("s3vectors: ListVectors repeated a vector key")
			}
			seenKeys[*row.Key] = struct{}{}
			doc, err := decodeDocument(*row.Key, row.Metadata)
			if err != nil {
				return nil, err
			}
			data, ok := row.Data.(*types.VectorDataMemberFloat32)
			if !ok || data == nil {
				return nil, errors.New("s3vectors: listed vector is missing float32 data")
			}
			if err = s.schema.validateVector(data.Value); err != nil {
				return nil, err
			}
			if predicate != nil {
				values, err := doc.Metadata.Values()
				if err != nil {
					return nil, err
				}
				matched, err := filter.Match(predicate, values)
				if err != nil {
					return nil, fmt.Errorf("s3vectors: evaluate Core predicate: %w", err)
				}
				if !matched {
					continue
				}
			}
			keys = append(keys, doc.ID)
		}
		if page.NextToken == nil || *page.NextToken == "" {
			return keys, nil
		}
		if _, repeated := seenTokens[*page.NextToken]; repeated {
			return nil, errors.New("s3vectors: ListVectors repeated a continuation token")
		}
		seenTokens[*page.NextToken] = struct{}{}
		input.NextToken = page.NextToken
	}
}

type rankedResult struct {
	distance float64
	result   *vectorstore.SearchResult
}

func (s *Store) Search(ctx context.Context, request *vectorstore.SearchRequest) (response *vectorstore.SearchResponse, err error) {
	if err = request.Validate(); err != nil {
		return nil, err
	}
	if err = request.Options.RequireMode(vectorstore.SearchModeSemantic); err != nil {
		return nil, err
	}
	if request.Options.ResultLimit() > MaxTopK {
		return nil, fmt.Errorf("s3vectors: %w: TopK exceeds %d", vectorstore.ErrInvalidOptions, MaxTopK)
	}
	defer func() {
		if err == nil {
			err = response.ValidateFor(request)
		}
		if err != nil {
			response = nil
		}
	}()
	keys, err := s.matchingKeys(ctx, request.Options.Filter)
	if err != nil {
		return nil, err
	}
	if len(keys) == 0 {
		return &vectorstore.SearchResponse{Results: []*vectorstore.SearchResult{}}, nil
	}
	vector, err := s.embeddingClient.EmbedText(ctx, request.Query)
	if err != nil {
		return nil, err
	}
	query, err := s.schema.narrow(vector)
	if err != nil {
		return nil, err
	}
	var groups [][]string
	if request.Options.Filter != nil {
		groups = slices.Collect(slices.Chunk(keys, maxQueryKeys))
	} else {
		groups = [][]string{nil}
	}
	var ranked []rankedResult
	seen := make(map[string]struct{})
	for _, keys := range groups {
		input := &awss3.QueryVectorsInput{VectorBucketName: aws.String(s.vectorBucketName), IndexName: aws.String(s.indexName), QueryVector: &types.VectorDataMemberFloat32{Value: query}, TopK: aws.Int32(int32(request.Options.ResultLimit())), ReturnDistance: true, ReturnMetadata: true}
		allowed := make(map[string]struct{}, len(keys))
		if keys != nil {
			for _, key := range keys {
				allowed[key] = struct{}{}
			}
			input.TopK = aws.Int32(int32(min(len(keys), request.Options.ResultLimit())))
			input.Filter = s3vdoc.NewLazyDocument(map[string]any{idMetaKey: map[string]any{"$in": keys}})
		}
		seenTokens := make(map[string]struct{})
		for {
			page, err := s.client.QueryVectors(ctx, input)
			if err != nil {
				return nil, fmt.Errorf("s3vectors: QueryVectors: %w", err)
			}
			if page == nil || page.Vectors == nil {
				return nil, errors.New("s3vectors: QueryVectors returned no vector array")
			}
			if page.DistanceMetric != s.schema.metric {
				return nil, fmt.Errorf("%w: native query metric changed", ErrIncompatibleIndex)
			}
			for _, row := range page.Vectors {
				if row.Key == nil || row.Distance == nil {
					return nil, errors.New("s3vectors: query result is missing key or distance")
				}
				if _, duplicate := seen[*row.Key]; duplicate {
					return nil, errors.New("s3vectors: QueryVectors repeated a vector key")
				}
				seen[*row.Key] = struct{}{}
				if keys != nil {
					if _, exists := allowed[*row.Key]; !exists {
						return nil, errors.New("s3vectors: native query returned a key outside Core membership")
					}
				}
				doc, err := decodeDocument(*row.Key, row.Metadata)
				if err != nil {
					return nil, err
				}
				if request.Options.Filter != nil {
					values, decodeErr := doc.Metadata.Values()
					if decodeErr != nil {
						return nil, decodeErr
					}
					matched, matchErr := filter.Match(request.Options.Filter, values)
					if matchErr != nil {
						return nil, matchErr
					}
					if !matched {
						return nil, errors.New("s3vectors: native result changed Core membership during search")
					}
				}
				distance := float64(*row.Distance)
				score, err := s.schema.score(distance)
				if err != nil {
					return nil, err
				}
				result, err := vectorstore.NewSearchResult(doc, score)
				if err != nil {
					return nil, err
				}
				ranked = append(ranked, rankedResult{distance: distance, result: result})
			}
			if page.NextToken == nil || *page.NextToken == "" {
				break
			}
			if _, repeated := seenTokens[*page.NextToken]; repeated {
				return nil, errors.New("s3vectors: QueryVectors repeated a continuation token")
			}
			seenTokens[*page.NextToken] = struct{}{}
			input.NextToken = page.NextToken
		}
	}
	slices.SortFunc(ranked, func(left, right rankedResult) int {
		if order := cmp.Compare(left.distance, right.distance); order != 0 {
			return order
		}
		return cmp.Compare(left.result.Document.ID, right.result.Document.ID)
	})
	results := make([]*vectorstore.SearchResult, 0, min(len(ranked), request.Options.ResultLimit()))
	for _, row := range ranked {
		if row.result.Score < request.Options.MinScore {
			continue
		}
		results = append(results, row.result)
		if len(results) == request.Options.ResultLimit() {
			break
		}
	}
	return &vectorstore.SearchResponse{Results: results}, nil
}

func (s *Store) DeleteIDs(ctx context.Context, ids []string) error {
	seen := make(map[string]struct{}, len(ids))
	keys := make([]string, 0, len(ids))
	for _, id := range ids {
		if err := validateKey(id); err != nil {
			return err
		}
		if _, duplicate := seen[id]; !duplicate {
			seen[id] = struct{}{}
			keys = append(keys, id)
		}
	}
	for chunk := range slices.Chunk(keys, MaxVectorsPerWrite) {
		output, err := s.client.DeleteVectors(ctx, &awss3.DeleteVectorsInput{VectorBucketName: aws.String(s.vectorBucketName), IndexName: aws.String(s.indexName), Keys: chunk})
		if err != nil {
			return fmt.Errorf("s3vectors: DeleteVectors: %w", err)
		}
		if output == nil {
			return errors.New("s3vectors: DeleteVectors returned no acknowledgment")
		}
	}
	return nil
}
