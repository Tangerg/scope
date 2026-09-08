package milvus

import (
	"testing"

	"github.com/milvus-io/milvus/client/v2/entity"

	"github.com/Tangerg/scope/core/vectorstore"
)

// Milvus returns the raw inner product for IP with no normalization, so it is
// unbounded unless the caller supplies unit vectors. Sharing the cosine mapping
// clamps every product outside [-1, 1] and collapses distinct results onto the
// same score.
func TestInnerProductScoresStayDistinctBeyondTheCosineRange(t *testing.T) {
	t.Parallel()

	store := &Store{metricType: entity.IP}
	products := []float64{-8, -2, -1, 0, 1, 2, 8}

	previous := store.normalizeScore(products[0])
	if err := previous.Validate(); err != nil {
		t.Fatalf("score for product %v: %v", products[0], err)
	}
	for _, product := range products[1:] {
		score := store.normalizeScore(product)
		if err := score.Validate(); err != nil {
			t.Fatalf("score for product %v: %v", product, err)
		}
		if score <= previous {
			t.Fatalf("score for product %v = %v, not above %v; ranking is lost", product, score, previous)
		}
		previous = score
	}
}

// COSINE is a similarity in [-1, 1] and keeps the mapping built for that range.
func TestCosineScoresSpanTheWholeRange(t *testing.T) {
	t.Parallel()

	store := &Store{metricType: entity.COSINE}
	for _, sample := range []struct {
		similarity float64
		want       vectorstore.Score
	}{
		{similarity: -1, want: 0},
		{similarity: 0, want: 0.5},
		{similarity: 1, want: 1},
	} {
		if got := store.normalizeScore(sample.similarity); got != sample.want {
			t.Fatalf("normalizeScore(%v) = %v, want %v", sample.similarity, got, sample.want)
		}
	}
}

// L2 is the squared distance, and ranking has to survive that.
func TestL2ScoresDecreaseWithDistance(t *testing.T) {
	t.Parallel()

	store := &Store{metricType: entity.L2}
	previous := store.normalizeScore(0)
	if previous != 1 {
		t.Fatalf("normalizeScore(0) = %v, want 1 for an exact match", previous)
	}
	for _, distance := range []float64{0.5, 1, 4, 100} {
		score := store.normalizeScore(distance)
		if score >= previous {
			t.Fatalf("score for distance %v = %v, not below %v", distance, score, previous)
		}
		previous = score
	}
}
