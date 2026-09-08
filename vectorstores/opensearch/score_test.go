package opensearch

import (
	"math"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/vectorstore"
)

// OpenSearch encodes an inner product as product+1 when positive and
// 1/(1-product) otherwise, so this store recovers the product before applying
// Core's normalization. Both branches of that encoding are strictly positive.
func TestInnerProductScoreRecoversTheProduct(t *testing.T) {
	t.Parallel()

	// The expectation comes from Core's own normalization applied to the
	// product each raw score encodes, so this pins the recovery rather than
	// restating the formula Core owns.
	tests := []struct {
		name    string
		raw     float64
		product float64
	}{
		{name: "positive product", raw: 4, product: 3},
		{name: "zero product", raw: 1, product: 0},
		{name: "negative product", raw: 0.5, product: -1},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			score, err := SpaceTypeIP.score(test.raw)
			if err != nil {
				t.Fatalf("score(%v) = %v, want nil", test.raw, err)
			}
			want := vectorstore.ScoreFromInnerProduct(test.product)
			if math.Abs(float64(score)-float64(want)) > 1e-12 {
				t.Fatalf("score(%v) = %v, want %v (the score of product %v)",
					test.raw, score, want, test.product)
			}
		})
	}
}

// A non-positive score cannot come from either branch of that encoding, so it
// is reported rather than read as a Core score. The recovery is only as good as
// the formula it inverts, and this is where a changed formula shows up instead
// of becoming a ranking nobody can explain.
func TestInnerProductScoreRefusesWhatTheEncodingCannotProduce(t *testing.T) {
	t.Parallel()

	for _, raw := range []float64{0, -1, -0.5} {
		_, err := SpaceTypeIP.score(raw)
		if err == nil {
			t.Fatalf("score(%v) = nil error, want an out-of-range error", raw)
		}
		if !strings.Contains(err.Error(), "outside the positive range") {
			t.Fatalf("score(%v) = %v, want an out-of-range error", raw, err)
		}
	}
}

// Every other space type reports a score OpenSearch already normalized, so it
// passes through untouched.
func TestOtherSpaceTypesPassTheScoreThrough(t *testing.T) {
	t.Parallel()

	for _, space := range []SpaceType{SpaceTypeCosine, SpaceTypeL2, SpaceTypeL1, SpaceTypeLInf} {
		score, err := space.score(0.25)
		if err != nil {
			t.Fatalf("%s.score() = %v, want nil", space, err)
		}
		if float64(score) != 0.25 {
			t.Fatalf("%s.score(0.25) = %v, want 0.25", space, score)
		}
	}
}
