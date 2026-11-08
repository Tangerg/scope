package couchbase

import (
	"fmt"
	"math"

	corevector "github.com/Tangerg/scope/core/vector"
	"github.com/Tangerg/scope/core/vectorstore"
)

type Similarity string

const (
	SimilarityCosine     Similarity = "COSINE"
	SimilarityL2Norm     Similarity = "L2"
	SimilarityDotProduct Similarity = "DOT"
)

func (s Similarity) Valid() bool {
	return s == SimilarityCosine || s == SimilarityL2Norm || s == SimilarityDotProduct
}

func (s Similarity) String() string { return string(s) }

func (s Similarity) score(distance float64) vectorstore.Score {
	switch s {
	case SimilarityCosine:
		return vectorstore.ScoreFromCosineDistance(distance)
	case SimilarityL2Norm:
		return vectorstore.ScoreFromDistance(distance)
	default:
		return vectorstore.ScoreFromNegativeInnerProductDistance(distance)
	}
}

type vectorPolicy struct {
	dimensions int
	similarity Similarity
}

func (v vectorPolicy) validate(values []float64) error {
	if len(values) != v.dimensions {
		return fmt.Errorf("couchbase: %w: expected %d coordinates, got %d", vectorstore.ErrInvalidRequest, v.dimensions, len(values))
	}
	if err := corevector.Coordinates[float64](values).Validate(); err != nil {
		return fmt.Errorf("couchbase: %w: %w", vectorstore.ErrInvalidRequest, err)
	}
	var squaredNorm float64
	for index, value := range values {
		// VECTOR_DISTANCE retains float64 arithmetic but returns NULL for
		// coordinates outside the native float32 range.
		if value < -math.MaxFloat32 || value > math.MaxFloat32 {
			return fmt.Errorf("couchbase: %w: coordinate %d is outside the native float32 range", vectorstore.ErrInvalidRequest, index)
		}
		squaredNorm += value * value
	}
	// A nonzero component can still underflow when the native cosine
	// calculation squares it, producing NULL for every comparison.
	if v.similarity == SimilarityCosine && squaredNorm == 0 {
		return fmt.Errorf("couchbase: %w: cosine requires a positive float64 squared norm", vectorstore.ErrInvalidRequest)
	}
	return nil
}
