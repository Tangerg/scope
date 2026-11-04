package s3vectors

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3vectors"
	"github.com/aws/aws-sdk-go-v2/service/s3vectors/schemas"
	"github.com/aws/aws-sdk-go-v2/service/s3vectors/types"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"
	"github.com/aws/smithy-go/transport/http/protocol/restjson1"

	"github.com/Tangerg/scope/core/vectorstore"
)

// SDK serialization owns escaping and envelope size. Planning every request
// before I/O also prevents an invalid late record from partially committing.
type putBatchPlan []*awss3.PutVectorsInput

func newPutBatchPlan(ctx context.Context, bucket, index string, records []types.PutInputVector) (putBatchPlan, error) {
	var plan putBatchPlan
	for group := range slices.Chunk(records, MaxVectorsPerWrite) {
		request := &awss3.PutVectorsInput{VectorBucketName: aws.String(bucket), IndexName: aws.String(index), Vectors: group}
		if err := plan.admit(ctx, request); err != nil {
			return nil, err
		}
	}
	return plan, nil
}

func (p *putBatchPlan) admit(ctx context.Context, input *awss3.PutVectorsInput) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	protocol := restjson1.New(schemas.S3Vectors)
	operation := smithy.NewOperationSchema(schemas.PutVectors, schemas.PutVectorsInput, schemas.PutVectorsOutput)
	request := smithyhttp.NewStackRequest().(*smithyhttp.Request)
	if err := protocol.SerializeRequest(ctx, operation, input, request); err != nil {
		return fmt.Errorf("s3vectors: encode put request: %w", err)
	}
	size, known, err := request.StreamLength()
	if err != nil {
		return fmt.Errorf("s3vectors: measure put request: %w", err)
	}
	if !known {
		return errors.New("s3vectors: SDK put request has no measurable body")
	}
	if size <= maxRequestBytes {
		*p = append(*p, input)
		return nil
	}
	if len(input.Vectors) == 1 {
		return fmt.Errorf("s3vectors: document %q exceeds the request budget: %w", *input.Vectors[0].Key, vectorstore.ErrInvalidDocument)
	}
	// Halving limits re-encoding to logarithmic depth within each 500-record
	// group instead of serializing a growing request after every append.
	middle := len(input.Vectors) / 2
	left, right := *input, *input
	left.Vectors, right.Vectors = input.Vectors[:middle], input.Vectors[middle:]
	if err := p.admit(ctx, &left); err != nil {
		return err
	}
	return p.admit(ctx, &right)
}
