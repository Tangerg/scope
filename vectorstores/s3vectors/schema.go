package s3vectors

import (
	"errors"
	"fmt"
	"math"
	"slices"

	"github.com/aws/aws-sdk-go-v2/service/s3vectors/types"

	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/vectorstore"
)

type indexSchema struct {
	dimensions int
	metric     types.DistanceMetric
}

func newIndexSchema(index *types.Index, bucket, name string) (indexSchema, error) {
	if index == nil || index.IndexName == nil || *index.IndexName != name || index.VectorBucketName == nil || *index.VectorBucketName != bucket || index.Dimension == nil || *index.Dimension < 1 || *index.Dimension > 4096 || index.DataType != types.DataTypeFloat32 || (index.DistanceMetric != types.DistanceMetricCosine && index.DistanceMetric != types.DistanceMetricEuclidean) || index.MetadataConfiguration == nil {
		return indexSchema{}, fmt.Errorf("%w: native identity, dimensions, data type or metric are invalid", ErrIncompatibleIndex)
	}
	keys := slices.Clone(index.MetadataConfiguration.NonFilterableMetadataKeys)
	slices.Sort(keys)
	if !slices.Equal(keys, []string{contentMetaKey, metadataMetaKey}) {
		return indexSchema{}, fmt.Errorf("%w: non-filterable keys must be exactly %s and %s", ErrIncompatibleIndex, contentMetaKey, metadataMetaKey)
	}
	return indexSchema{dimensions: int(*index.Dimension), metric: index.DistanceMetric}, nil
}

func (i indexSchema) narrow(vector []float64) ([]float32, error) {
	narrowed := embedding.Float32Vector(vector)
	if err := i.validateVector(narrowed); err != nil {
		return nil, err
	}
	return narrowed, nil
}

func (i indexSchema) validateVector(vector []float32) error {
	if len(vector) != i.dimensions {
		return fmt.Errorf("s3vectors: vector dimension %d differs from native dimension %d", len(vector), i.dimensions)
	}
	nonzero := false
	for _, component := range vector {
		if math.IsNaN(float64(component)) || math.IsInf(float64(component), 0) {
			return errors.New("s3vectors: vector is not finite float32")
		}
		if component != 0 {
			nonzero = true
		}
	}
	if i.metric == types.DistanceMetricCosine && !nonzero {
		return errors.New("s3vectors: cosine vector became all zero in native float32")
	}
	return nil
}

func (i indexSchema) score(distance float64) (vectorstore.Score, error) {
	if math.IsNaN(distance) || math.IsInf(distance, 0) || distance < 0 {
		return 0, errors.New("s3vectors: native distance must be finite and nonnegative")
	}
	switch i.metric {
	case types.DistanceMetricCosine:
		if distance > 2 {
			return 0, errors.New("s3vectors: native cosine distance exceeds 2")
		}
		return vectorstore.ScoreFromCosineDistance(distance), nil
	case types.DistanceMetricEuclidean:
		return vectorstore.ScoreFromDistance(distance), nil
	default:
		return 0, ErrIncompatibleIndex
	}
}
