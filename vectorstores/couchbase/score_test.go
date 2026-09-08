package couchbase

import (
	"math"
	"testing"
)

// Couchbase publishes no formula for its relevance score and the score is not
// confined to Core's range: the documented example response for a vector query
// returns 3.4028234663852886e+38 — float32's maximum — alongside
// 0.42046520427629075 and 0.0004977600796416127. Clamping those collapsed every
// score above 1 onto 1, so this pins that the order survives instead.
func TestScoreFromRelevancePreservesOrder(t *testing.T) {
	t.Parallel()

	// The three scores the REST documentation's own example returns, in the
	// order Couchbase ranked them.
	documented := []float64{3.4028234663852886e+38, 0.42046520427629075, 0.0004977600796416127}
	previous := math.Inf(1)
	for _, raw := range documented {
		score := scoreFromRelevance(raw)
		if float64(score) >= previous {
			t.Fatalf("scoreFromRelevance(%v) = %v, want less than the previous %v", raw, score, previous)
		}
		if score < 0 || score > 1 {
			t.Fatalf("scoreFromRelevance(%v) = %v, want a score within [0, 1]", raw, score)
		}
		previous = float64(score)
	}

	// The clamp this replaced made these two the same number.
	high, low := scoreFromRelevance(1e6), scoreFromRelevance(5)
	if !(high > low) {
		t.Fatalf("scoreFromRelevance(1e6) = %v and scoreFromRelevance(5) = %v, want the larger score to rank higher",
			high, low)
	}
}

// A dot_product field holding vectors that are not unit length can score
// negative, which is no similarity rather than a rank to preserve. NaN and
// +Inf cannot reach a Core score either.
func TestScoreFromRelevanceBoundsWhatIsNotARank(t *testing.T) {
	t.Parallel()

	for _, raw := range []float64{0, -1, -1e30, math.NaN()} {
		if score := scoreFromRelevance(raw); score != 0 {
			t.Fatalf("scoreFromRelevance(%v) = %v, want 0", raw, score)
		}
	}
	if score := scoreFromRelevance(math.Inf(1)); score != 1 {
		t.Fatalf("scoreFromRelevance(+Inf) = %v, want 1", score)
	}
	// float32's maximum is the value Couchbase's own example reports for what
	// must be an exact match, so it has to reach the top of the range.
	if score := scoreFromRelevance(math.MaxFloat32); float64(score) < 0.999999 {
		t.Fatalf("scoreFromRelevance(MaxFloat32) = %v, want it at the top of the range", score)
	}
}
