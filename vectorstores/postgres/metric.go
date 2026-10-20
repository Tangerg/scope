package postgres

import (
	"errors"
	"math"

	pgvec "github.com/pgvector/pgvector-go"

	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/vectorstore"
)

type DistanceMetric string

const (
	DistanceCosine DistanceMetric = "cosine"
	DistanceL2     DistanceMetric = "l2"
	DistanceIP     DistanceMetric = "ip"
)

func (d DistanceMetric) Valid() bool {
	operator, _ := d.nativeProjection()
	return operator != ""
}
func (d DistanceMetric) String() string { return string(d) }

// Operator returns the pgwire distance operator and panics for an invalid metric.
func (d DistanceMetric) Operator() string {
	operator, _ := d.nativeProjection()
	if operator == "" {
		panic("invalid constructed distance metric")
	}
	return operator
}
func (d DistanceMetric) Score(value float64) (vectorstore.Score, error) {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, errors.New("postgres: non-finite native distance")
	}
	switch d {
	case DistanceCosine:
		if value < 0 || value > 2 {
			return 0, errors.New("postgres: cosine distance is outside [0,2]")
		}
		return vectorstore.ScoreFromCosineDistance(value), nil
	case DistanceL2:
		if value < 0 {
			return 0, errors.New("postgres: negative Euclidean distance")
		}
		return vectorstore.ScoreFromDistance(value), nil
	case DistanceIP:
		return vectorstore.ScoreFromNegativeInnerProductDistance(value), nil
	}
	return 0, errors.New("postgres: invalid native distance metric")
}
func (d DistanceMetric) Vector(values []float64, width int) (pgvec.Vector, error) {
	if !d.Valid() {
		return pgvec.Vector{}, errors.New("postgres: invalid native distance metric")
	}
	if len(values) != width || width <= 0 {
		return pgvec.Vector{}, errors.New("postgres: vector dimensions disagree with native schema")
	}
	narrowed := embedding.Float32Vector(values)
	nonzero := false
	for index, value := range values {
		converted := float64(narrowed[index])
		if math.IsNaN(value) || math.IsInf(value, 0) || math.IsInf(converted, 0) || (value != 0 && converted == 0) {
			return pgvec.Vector{}, errors.New("postgres: vector is not representable in float32")
		}
		nonzero = nonzero || converted != 0
	}
	if d == DistanceCosine && !nonzero {
		return pgvec.Vector{}, errors.New("postgres: cosine requires a nonzero float32 vector")
	}
	return pgvec.NewVector(narrowed), nil
}

// IndexOpClass returns the native vector index class and panics for an invalid metric.
func (d DistanceMetric) IndexOpClass() string {
	_, opclass := d.nativeProjection()
	if opclass == "" {
		panic("invalid constructed distance metric")
	}
	return opclass
}

func (d DistanceMetric) nativeProjection() (operator, opclass string) {
	switch d {
	case DistanceCosine:
		return "<=>", "vector_cosine_ops"
	case DistanceL2:
		return "<->", "vector_l2_ops"
	case DistanceIP:
		return "<#>", "vector_ip_ops"
	}
	return "", ""
}
