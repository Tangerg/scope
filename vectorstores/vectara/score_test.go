package vectara

import (
	"math"
	"strings"
	"testing"
)

// Vectara documents the scale for the query this store sends: "results from
// Vectara are scored on a scale from -1 to 1, with 1 being a perfect match and
// -1 having absolutely nothing to do with the query". Passing that through as
// if it were already a Core score understated every result and flattened the
// negative half onto zero.
func TestRelevanceScoreMapsTheDocumentedScale(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  float64
		want float64
	}{
		{name: "perfect match", raw: 1, want: 1},
		{name: "nothing to do with the query", raw: -1, want: 0},
		{name: "midpoint", raw: 0, want: 0.5},
		// The value ScoreFromValue used to report as 0.5.
		{name: "three quarters", raw: 0.5, want: 0.75},
		{name: "negative keeps its order", raw: -0.5, want: 0.25},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			score, err := relevanceScore(test.raw)
			if err != nil {
				t.Fatalf("relevanceScore(%v) = %v, want nil", test.raw, err)
			}
			if math.Abs(float64(score)-test.want) > 1e-12 {
				t.Fatalf("relevanceScore(%v) = %v, want %v", test.raw, score, test.want)
			}
		})
	}
}

// A reranker is corpus configuration this store does not set, and Vectara
// documents reranked scores as unbounded. An out-of-scale score therefore means
// the scale this store maps no longer applies, and squeezing it onto the bound
// would hide that behind a plausible number.
func TestRelevanceScoreRefusesAnUnboundedScore(t *testing.T) {
	t.Parallel()

	for _, raw := range []float64{1.0001, 5, -5, 10, math.NaN()} {
		_, err := relevanceScore(raw)
		if err == nil {
			t.Fatalf("relevanceScore(%v) = nil error, want an out-of-scale error", raw)
		}
		if !strings.Contains(err.Error(), "outside the documented") {
			t.Fatalf("relevanceScore(%v) = %v, want an out-of-scale error", raw, err)
		}
	}
}
