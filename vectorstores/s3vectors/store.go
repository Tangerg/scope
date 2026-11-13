package s3vectors

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3vectors"

	"github.com/aws/aws-sdk-go-v2/service/s3vectors/types"
	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/document"

	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

const (
	Provider               = "S3Vectors"
	MaxTopK                = 10_000
	MaxResultsPerQueryPage = 100
	MaxVectorsPerWrite     = 500
	maxRequestBytes        = 20 * 1024 * 1024
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
	Space            vectorstore.SpaceID
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
	return s.Space.Validate()
}

var (
	_ vectorstore.Upserter  = (*Store)(nil)
	_ vectorstore.Querier   = (*Store)(nil)
	_ vectorstore.IDDeleter = (*Store)(nil)
)

type Store struct {
	client           VectorClient
	vectorBucketName string
	indexName        string
	space            vectorstore.SpaceID
	schema           indexSchema
}

func NewStore(ctx context.Context, config StoreConfig) (*Store, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	store := &Store{client: config.Client, vectorBucketName: config.VectorBucketName, indexName: config.IndexName, space: config.Space}
	schema, err := store.readSchema(ctx)
	if err != nil {
		return nil, err
	}
	store.schema = schema
	return store, nil
}

func (s *Store) readSchema(ctx context.Context) (indexSchema, error) {
	output, err := s.client.GetIndex(ctx, &awss3.GetIndexInput{VectorBucketName: aws.String(s.vectorBucketName), IndexName: aws.String(s.indexName)})
	if err != nil {
		return indexSchema{}, fmt.Errorf("s3vectors: GetIndex: %w", err)
	}
	if output == nil {
		return indexSchema{}, fmt.Errorf("%w: GetIndex returned no response", ErrIncompatibleIndex)
	}
	return newIndexSchema(output.Index, s.vectorBucketName, s.indexName, s.space)
}

func (s *Store) checkBinding(ctx context.Context) error {
	got, err := s.readSchema(ctx)
	if err != nil {
		return err
	}
	if got != s.schema {
		return ErrIncompatibleIndex
	}
	return nil
}

// CheckDocuments verifies metadata budgets, media support and the physical
// index binding before a composition requests remote embeddings.
func (s *Store) CheckDocuments(ctx context.Context, space vectorstore.SpaceID, docs []*document.Document) error {
	if err := space.Require(s.space); err != nil {
		return err
	}
	if err := vectorstore.ValidateDocuments(docs); err != nil {
		return err
	}
	for _, doc := range docs {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := encodeDocument(doc); err != nil {
			return err
		}
	}
	return s.checkBinding(ctx)
}

func (s *Store) Upsert(ctx context.Context, batch *vectorstore.Batch) error {
	if err := batch.ValidateFor(s.space); err != nil {
		return err
	}
	records := make([]types.PutInputVector, len(batch.Records))
	for i, r := range batch.Records {
		if err := ctx.Err(); err != nil {
			return err
		}
		data, err := s.schema.narrow(r.Values)
		if err != nil {
			return err
		}
		md, err := encodeDocument(r.Document)
		if err != nil {
			return err
		}
		records[i] = types.PutInputVector{Key: aws.String(r.Document.ID), Data: &types.VectorDataMemberFloat32{Value: data}, Metadata: md}
	}
	requests, err := newPutBatchPlan(ctx, s.vectorBucketName, s.indexName, records)
	if err != nil {
		return err
	}
	if err = s.checkBinding(ctx); err != nil {
		return err
	}
	keys := make([]string, len(records))
	for i, v := range records {
		keys[i] = *v.Key
	}
	offset := 0
	for _, req := range requests {
		out, err := s.client.PutVectors(ctx, req)
		if err != nil || out == nil {
			if err == nil {
				err = errors.New("s3vectors: missing put acknowledgment")
			}
			return incompleteWrite(keys, offset, len(req.Vectors), err)
		}
		offset += len(req.Vectors)
	}

	return nil
}

// Scan evaluates predicate over pages the caller is authorized to enumerate.
// MaxBytes counts returned key, encoded metadata and float32 coordinates;
// service pagination provides no snapshot isolation. No partial result is returned.
func (s *Store) Scan(ctx context.Context, request vectorstore.ScanRequest) ([]*document.Document, error) {
	if err := request.Validate(); err != nil {
		return nil, err
	}
	meter, err := request.Budget.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer meter.Close()
	ctx = meter.Context()
	if err := s.checkBinding(ctx); err != nil {
		return nil, err
	}
	var docs []*document.Document
	seenKeys := make(map[string]struct{})
	seenTokens := make(map[string]struct{})
	// One record past the grant proves that the source exceeds it.
	input := &awss3.ListVectorsInput{VectorBucketName: aws.String(s.vectorBucketName), IndexName: aws.String(s.indexName), MaxResults: aws.Int32(int32(min(1000, request.Budget.MaxRecords+1))), ReturnMetadata: true, ReturnData: true}
	for {
		page, err := s.client.ListVectors(ctx, input)
		if err != nil {
			return nil, fmt.Errorf("s3vectors: ListVectors: %w", err)
		}
		if page == nil || page.Vectors == nil {
			return nil, errors.New("s3vectors: ListVectors returned no vector array")
		}
		for _, row := range page.Vectors {
			doc, err := s.admitListed(meter, row, seenKeys)
			if err != nil {
				return nil, err
			}
			if request.Filter != nil {
				matched, err := filter.Match(request.Filter, doc.Metadata)
				if err != nil {
					return nil, fmt.Errorf("s3vectors: evaluate Core predicate: %w", err)
				}
				if !matched {
					continue
				}
			}
			docs = append(docs, doc)
		}
		if page.NextToken == nil || *page.NextToken == "" {
			return docs, nil
		}
		if _, repeated := seenTokens[*page.NextToken]; repeated {
			return nil, errors.New("s3vectors: ListVectors repeated a continuation token")
		}
		seenTokens[*page.NextToken] = struct{}{}
		input.NextToken = page.NextToken
	}
}

// admitListed charges one listed vector's key, encoded metadata and float32
// coordinates before decoding it, and rejects a repeated key.
func (s *Store) admitListed(meter *vectorstore.ScanMeter, row types.ListOutputVector, seen map[string]struct{}) (*document.Document, error) {
	if row.Key == nil {
		return nil, errors.New("s3vectors: listed vector is missing its key")
	}
	if _, duplicate := seen[*row.Key]; duplicate {
		return nil, errors.New("s3vectors: ListVectors repeated a vector key")
	}
	seen[*row.Key] = struct{}{}
	data, ok := row.Data.(*types.VectorDataMemberFloat32)
	if !ok || data == nil {
		return nil, errors.New("s3vectors: listed vector is missing float32 data")
	}
	raw, err := row.Metadata.MarshalSmithyDocument()
	if err != nil {
		return nil, err
	}
	if err = meter.Charge(int64(len(raw)) + int64(len(*row.Key)) + 4*int64(len(data.Value))); err != nil {
		return nil, err
	}
	doc, err := decodeDocument(*row.Key, row.Metadata)
	if err != nil {
		return nil, err
	}
	if err := s.schema.validateVector(data.Value); err != nil {
		return nil, err
	}
	return doc, nil
}

type rankedResult struct {
	distance float64
	result   *vectorstore.SearchResult
}

// admitOptions holds validated options to what one native QueryVectors call
// can answer; it carries no metadata predicate.
func admitOptions(opts vectorstore.SearchOptions) error {
	if err := opts.RequireMode(vectorstore.SearchModeSemantic); err != nil {
		return err
	}
	if err := opts.RequireResultLimit(MaxTopK); err != nil {
		return err
	}
	if opts.Filter != nil {
		return vectorstore.ErrUnsupportedFilter
	}
	return nil
}

func (s *Store) CheckQuery(ctx context.Context, space vectorstore.SpaceID, opts vectorstore.SearchOptions) error {
	if err := space.Require(s.space); err != nil {
		return err
	}
	if err := opts.Validate(); err != nil {
		return err
	}
	if err := admitOptions(opts); err != nil {
		return err
	}
	return s.checkBinding(ctx)
}

func (s *Store) Query(ctx context.Context, request *vectorstore.Query) (response *vectorstore.SearchResponse, err error) {
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if err = request.ValidateFor(s.space); err != nil {
		return nil, err
	}
	if err = admitOptions(request.Options); err != nil {
		return nil, err
	}
	if err = s.checkBinding(ctx); err != nil {
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
	query, err := s.schema.narrow(request.Values)
	if err != nil {
		return nil, err
	}
	ranked, err := s.queryPages(ctx, query, request.Options.ResultLimit())
	if err != nil {
		return nil, err
	}
	slices.SortFunc(ranked, func(left, right rankedResult) int {
		return cmp.Or(cmp.Compare(left.distance, right.distance), cmp.Compare(left.result.Document.ID, right.result.Document.ID))
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

// queryPages follows QueryVectors continuation tokens and rejects a repeated
// key or token, either of which would make the native result incomplete.
func (s *Store) queryPages(ctx context.Context, query []float32, limit int) ([]rankedResult, error) {
	input := &awss3.QueryVectorsInput{VectorBucketName: aws.String(s.vectorBucketName), IndexName: aws.String(s.indexName), QueryVector: &types.VectorDataMemberFloat32{Value: query}, TopK: aws.Int32(int32(limit)), ReturnDistance: true, ReturnMetadata: true}
	var ranked []rankedResult
	seen := make(map[string]struct{})
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
			result, err := s.rankRow(row, seen)
			if err != nil {
				return nil, err
			}
			ranked = append(ranked, result)
		}
		if page.NextToken == nil || *page.NextToken == "" {
			return ranked, nil
		}
		if _, repeated := seenTokens[*page.NextToken]; repeated {
			return nil, errors.New("s3vectors: QueryVectors repeated a continuation token")
		}
		seenTokens[*page.NextToken] = struct{}{}
		input.NextToken = page.NextToken
	}
}

func (s *Store) rankRow(row types.QueryOutputVector, seen map[string]struct{}) (rankedResult, error) {
	if row.Key == nil || row.Distance == nil {
		return rankedResult{}, errors.New("s3vectors: query result is missing key or distance")
	}
	if _, duplicate := seen[*row.Key]; duplicate {
		return rankedResult{}, errors.New("s3vectors: QueryVectors repeated a vector key")
	}
	seen[*row.Key] = struct{}{}
	doc, err := decodeDocument(*row.Key, row.Metadata)
	if err != nil {
		return rankedResult{}, err
	}
	distance := float64(*row.Distance)
	score, err := s.schema.score(distance)
	if err != nil {
		return rankedResult{}, err
	}
	result, err := vectorstore.NewSearchResult(doc, score)
	if err != nil {
		return rankedResult{}, err
	}
	return rankedResult{distance: distance, result: result}, nil
}

func (s *Store) DeleteIDs(ctx context.Context, ids []string) error {
	keys, err := vectorstore.DistinctIDs(ids)
	if err != nil {
		return err
	}
	for _, id := range keys {
		if err := validateKey(id); err != nil {
			return err
		}
	}
	if len(keys) == 0 {
		return nil
	}
	if err := s.checkBinding(ctx); err != nil {
		return err
	}
	offset := 0
	for chunk := range slices.Chunk(keys, MaxVectorsPerWrite) {
		output, err := s.client.DeleteVectors(ctx, &awss3.DeleteVectorsInput{VectorBucketName: aws.String(s.vectorBucketName), IndexName: aws.String(s.indexName), Keys: chunk})
		if err != nil {
			return incompleteWrite(keys, offset, len(chunk), fmt.Errorf("s3vectors: DeleteVectors: %w", err))
		}
		if output == nil {
			return incompleteWrite(keys, offset, len(chunk), errors.New("s3vectors: DeleteVectors returned no acknowledgment"))
		}
		offset += len(chunk)
	}
	return nil
}

func incompleteWrite(keys []string, begin, n int, cause error) error {
	return vectorstore.NewWriteError(cause, vectorstore.WriteOutcome{Acknowledged: keys[:begin], Unconfirmed: keys[begin : begin+n], Unattempted: keys[begin+n:]})
}
