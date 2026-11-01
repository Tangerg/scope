package vectorstore_test

import (
	"math"
	"testing"

	"github.com/Tangerg/scope/core/vectorstore"
)

func TestScoreNormalization(t *testing.T) {
	tests := []struct {
		name string
		got  vectorstore.Score
		want vectorstore.Score
	}{
		{"opposite similarity", vectorstore.ScoreFromCosineSimilarity(-1), 0},
		{"orthogonal similarity", vectorstore.ScoreFromCosineSimilarity(0), 0.5},
		{"identical similarity", vectorstore.ScoreFromCosineSimilarity(1), 1},
		{"identical distance", vectorstore.ScoreFromCosineDistance(0), 1},
		{"orthogonal distance", vectorstore.ScoreFromCosineDistance(1), 0.5},
		{"opposite distance", vectorstore.ScoreFromCosineDistance(2), 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.got != test.want {
				t.Fatalf("score = %v, want %v", test.got, test.want)
			}
		})
	}
}

func TestDistanceNormalizationIsMonotonic(t *testing.T) {
	if got := vectorstore.ScoreFromDistance(0); got != 1 {
		t.Fatalf("exact-match score = %v, want 1", got)
	}
	if near, far := vectorstore.ScoreFromDistance(1), vectorstore.ScoreFromDistance(2); near <= far {
		t.Fatalf("near score %v must exceed far score %v", near, far)
	}
}

func TestInnerProductNormalizationIsStableAndMonotonic(t *testing.T) {
	negative := vectorstore.ScoreFromInnerProduct(-1000)
	zero := vectorstore.ScoreFromInnerProduct(0)
	positive := vectorstore.ScoreFromInnerProduct(1000)
	if !(negative < zero && zero < positive) {
		t.Fatalf("scores are not monotonic: %v, %v, %v", negative, zero, positive)
	}
	if zero != 0.5 || negative != 0 || positive != 1 {
		t.Fatalf("unexpected scores: %v, %v, %v", negative, zero, positive)
	}
}

func TestNonFiniteScoresRemainInvalid(t *testing.T) {
	for _, score := range []vectorstore.Score{
		vectorstore.ScoreFromValue(math.Inf(1)),
		vectorstore.ScoreFromDistance(math.Inf(1)),
		vectorstore.ScoreFromInnerProduct(math.Inf(1)),
	} {
		if !math.IsNaN(score.Float64()) {
			t.Fatalf("score = %v, want NaN", score)
		}
	}
}

func TestImpossibleNativeDistancesNeverProduceSuccessfulMatches(t *testing.T) {
	for _, s := range []vectorstore.Score{
		vectorstore.ScoreFromDistance(-1), vectorstore.ScoreFromCosineDistance(-math.SmallestNonzeroFloat64),
		vectorstore.ScoreFromCosineDistance(math.Nextafter(2, 3)), vectorstore.ScoreFromCosineSimilarity(-2),
		vectorstore.ScoreFromCosineSimilarity(math.Nextafter(1, 2)),
	} {
		if err := s.Validate(); err == nil {
			t.Fatalf("invalid metric became valid ranking %g", s)
		}
	}
}
