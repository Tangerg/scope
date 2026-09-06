package inmemory

import (
	"math"
	"math/big"
	"math/bits"

	"github.com/Tangerg/scope/core/vectorstore"
)

// Similarity scores two equal-length vectors; higher means more
// similar. Implementations must be deterministic and symmetric:
// Similarity(a, b) == Similarity(b, a). Returning [vectorstore.Score] keeps
// custom strategies inside the same normalized contract as every provider.
type Similarity func(left, right []float64) vectorstore.Score

// CosineSimilarity is the default for [StoreConfig.Similarity] —
// cos(θ) mapped into [0, 1] via (1 + cos) / 2. Returns 0.5 (the
// "no information" midpoint) when either vector has zero magnitude
// rather than NaN.
func CosineSimilarity(left, right []float64) vectorstore.Score {
	if len(left) != len(right) || len(left) == 0 {
		return 0
	}
	var leftScale, rightScale float64
	for index := range left {
		leftScale = max(leftScale, math.Abs(left[index]))
		rightScale = max(rightScale, math.Abs(right[index]))
	}
	if scale := max(leftScale, rightScale); math.IsNaN(scale) || math.IsInf(scale, 0) {
		return vectorstore.Score(math.NaN())
	}
	if leftScale == 0 || rightScale == 0 {
		return vectorstore.ScoreFromCosineSimilarity(0)
	}
	// Independent scales cancel in cosine without squaring the original range.
	var dot, magA, magB float64
	for index := range left {
		a, b := left[index]/leftScale, right[index]/rightScale
		dot += a * b
		magA += a * a
		magB += b * b
	}
	return vectorstore.ScoreFromCosineSimilarity(dot / (math.Sqrt(magA) * math.Sqrt(magB)))
}

// DotProductSimilarity maps the unbounded inner product monotonically into
// the common score range when vector magnitude is meaningful. Products and
// their sum retain their exact values until the final score conversion.
func DotProductSimilarity(left, right []float64) vectorstore.Score {
	if len(left) != len(right) {
		return 0
	}
	_, largestExponent := math.Frexp(math.MaxFloat64)
	_, smallestExponent := math.Frexp(math.SmallestNonzeroFloat64)
	// Every finite float64 product fits this binary range. The count supplies
	// carry bits, so cancellation cannot discard any product's low bits.
	precision := uint(2*(largestExponent-smallestExponent+1) + bits.Len(uint(len(left))))
	var a, b, product big.Float
	product.SetPrec(precision)
	sum := new(big.Float).SetPrec(precision)
	next := new(big.Float).SetPrec(precision)
	for index := range left {
		if math.IsNaN(left[index]) || math.IsNaN(right[index]) || math.IsInf(left[index], 0) || math.IsInf(right[index], 0) {
			return vectorstore.Score(math.NaN())
		}
		next.Add(sum, product.Mul(a.SetFloat64(left[index]), b.SetFloat64(right[index])))
		sum, next = next, sum
	}
	dot, _ := sum.Float64()
	// A finite exact sum beyond float64 already rounds the logistic score to
	// its endpoint. Individual overflowing products must cancel before this.
	if math.IsInf(dot, 1) {
		return 1
	}
	if math.IsInf(dot, -1) {
		return 0
	}
	return vectorstore.ScoreFromInnerProduct(dot)
}

// EuclideanSimilarity maps Euclidean distance into [0, 1] via
// 1 / (1 + d). Useful when the embedding space is *not* angular and
// magnitude differences carry information.
func EuclideanSimilarity(left, right []float64) vectorstore.Score {
	if len(left) != len(right) {
		return 0
	}
	var scale float64
	for index := range left {
		scale = max(scale, math.Abs(left[index]), math.Abs(right[index]))
	}
	if math.IsNaN(scale) || math.IsInf(scale, 0) {
		return vectorstore.Score(math.NaN())
	}
	_, exponent := math.Frexp(scale)
	var distance float64
	for index := range left {
		difference := math.Ldexp(left[index], -exponent) - math.Ldexp(right[index], -exponent)
		distance = math.Hypot(distance, difference)
	}
	if exponent <= 0 {
		return vectorstore.ScoreFromDistance(math.Ldexp(distance, exponent))
	}
	// Divide the score formula by the scale so the original distance need not
	// fit in float64. Binary scaling also preserves nearby large coordinates.
	inverseScale := math.Ldexp(1, -exponent)
	return vectorstore.Score(inverseScale / (inverseScale + distance))
}
