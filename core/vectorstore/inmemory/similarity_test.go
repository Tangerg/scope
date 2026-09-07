package inmemory_test

import (
	"context"
	"math"
	"testing"

	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/inmemory"
)

// TestSimilarityFunctionsShareTheNormalizedContract keeps every strategy inside
// the same [0, 1] score range providers are held to, so swapping the strategy
// cannot change what MinScore means.
func TestSimilarityFunctionsShareTheNormalizedContract(t *testing.T) {
	strategies := map[string]inmemory.Similarity{
		"cosine":      inmemory.CosineSimilarity,
		"dot product": inmemory.DotProductSimilarity,
		"euclidean":   inmemory.EuclideanSimilarity,
	}
	vectors := [][2][]float64{
		{{1, 0, 0}, {1, 0, 0}},
		{{1, 0, 0}, {0, 1, 0}},
		{{1, 0, 0}, {-1, 0, 0}},
		{{0.5, 0.5}, {0.25, 0.75}},
		{{1000, -1000}, {-1000, 1000}},
	}
	for name, strategy := range strategies {
		t.Run(name, func(t *testing.T) {
			for _, pair := range vectors {
				score := strategy(pair[0], pair[1])
				if err := score.Validate(); err != nil {
					t.Fatalf("%v vs %v scored %v: %v", pair[0], pair[1], score, err)
				}
			}
		})
	}
}

// TestSimilarityIsSymmetric is part of the [inmemory.Similarity] contract: an
// asymmetric strategy would make result ordering depend on map iteration order.
func TestSimilarityIsSymmetric(t *testing.T) {
	left := []float64{0.1, -0.4, 0.9}
	right := []float64{0.7, 0.2, -0.3}
	strategies := map[string]inmemory.Similarity{
		"cosine":      inmemory.CosineSimilarity,
		"dot product": inmemory.DotProductSimilarity,
		"euclidean":   inmemory.EuclideanSimilarity,
	}
	for name, strategy := range strategies {
		t.Run(name, func(t *testing.T) {
			if strategy(left, right) != strategy(right, left) {
				t.Fatalf("%s is not symmetric", name)
			}
		})
	}
}

// TestSimilarityRejectsMismatchedVectors keeps a dimension mismatch from
// producing a partial score: an incomparable pair must score zero, not the
// similarity of its shared prefix.
func TestSimilarityRejectsMismatchedVectors(t *testing.T) {
	strategies := map[string]inmemory.Similarity{
		"cosine":      inmemory.CosineSimilarity,
		"dot product": inmemory.DotProductSimilarity,
		"euclidean":   inmemory.EuclideanSimilarity,
	}
	for name, strategy := range strategies {
		t.Run(name, func(t *testing.T) {
			if score := strategy([]float64{1, 2}, []float64{1}); score != 0 {
				t.Fatalf("%s scored mismatched vectors %v", name, score)
			}
		})
	}
	if score := inmemory.CosineSimilarity(nil, nil); score != 0 {
		t.Fatalf("cosine scored empty vectors %v", score)
	}
}

// TestCosineSimilarityHandlesZeroMagnitude documents why a zero vector scores
// the midpoint rather than NaN: an unscoreable pair must still sort predictably
// instead of poisoning the comparison.
func TestCosineSimilarityHandlesZeroMagnitude(t *testing.T) {
	score := inmemory.CosineSimilarity([]float64{0, 0}, []float64{1, 1})
	if err := score.Validate(); err != nil {
		t.Fatal(err)
	}
	if score != vectorstore.ScoreFromCosineSimilarity(0) {
		t.Fatalf("zero-magnitude score = %v, want the no-information midpoint", score)
	}
	if math.IsNaN(float64(score)) {
		t.Fatal("zero-magnitude vectors produced NaN")
	}
}

// TestSimilarityOrdersByCloseness is the property search depends on: the more
// similar pair must score strictly higher under every strategy.
func TestSimilarityOrdersByCloseness(t *testing.T) {
	query := []float64{1, 0}
	near := []float64{0.9, 0.1}
	far := []float64{-1, 0}
	strategies := map[string]inmemory.Similarity{
		"cosine":      inmemory.CosineSimilarity,
		"dot product": inmemory.DotProductSimilarity,
		"euclidean":   inmemory.EuclideanSimilarity,
	}
	for name, strategy := range strategies {
		t.Run(name, func(t *testing.T) {
			if strategy(query, near) <= strategy(query, far) {
				t.Fatalf("%s did not rank the closer vector higher", name)
			}
		})
	}
}

func TestCosineSimilarityPreservesScaleAndDirection(t *testing.T) {
	for name, scale := range map[string]float64{
		"ordinary": 1, "large": 1e200, "small": 1e-200,
		"largest": math.MaxFloat64, "smallest": math.SmallestNonzeroFloat64,
	} {
		t.Run(name, func(t *testing.T) {
			left := []float64{scale, scale}
			for direction, testCase := range map[string]struct {
				right []float64
				want  float64
			}{
				"parallel":   {right: []float64{1, 1}, want: 1},
				"orthogonal": {right: []float64{-1, 1}, want: 0.5},
				"opposite":   {right: []float64{-1, -1}, want: 0},
				"zero":       {right: []float64{0, 0}, want: 0.5},
			} {
				t.Run(direction, func(t *testing.T) {
					got := inmemory.CosineSimilarity(left, testCase.right)
					if err := got.Validate(); err != nil || math.Abs(float64(got)-testCase.want) > 1e-15 {
						t.Fatalf("score = %g (%v), want %g", got, err, testCase.want)
					}
				})
			}
		})
	}
}

func TestEuclideanSimilarityPreservesRepresentableScores(t *testing.T) {
	for name, testCase := range map[string]struct {
		left, right []float64
		want        float64
	}{
		"large distance": {left: []float64{1e200}, right: []float64{0}, want: 1e-200},
		"opposite limits": {
			left: []float64{math.MaxFloat64}, right: []float64{-math.MaxFloat64}, want: (1 / math.MaxFloat64) / 2,
		},
		"shared large coordinate": {
			left: []float64{math.MaxFloat64, 1}, right: []float64{math.MaxFloat64, 0}, want: 0.5,
		},
		"small distance": {left: []float64{1e-200}, right: []float64{0}, want: 1},
	} {
		t.Run(name, func(t *testing.T) {
			got := inmemory.EuclideanSimilarity(testCase.left, testCase.right)
			if err := got.Validate(); err != nil || math.Abs(float64(got)/testCase.want-1) > 1e-14 {
				t.Fatalf("score = %g (%v), want %g", got, err, testCase.want)
			}
		})
	}
}

func TestDotProductSimilarityNormalizesAfterCancellation(t *testing.T) {
	for name, testCase := range map[string]struct {
		left, right []float64
		want        vectorstore.Score
	}{
		"positive overflow": {left: []float64{1e200}, right: []float64{1e200}, want: 1},
		"negative overflow": {left: []float64{1e200}, right: []float64{-1e200}, want: 0},
		"cancellation": {
			left:  []float64{math.MaxFloat64, math.MaxFloat64},
			right: []float64{math.MaxFloat64, -math.MaxFloat64}, want: 0.5,
		},
		"residual after cancellation": {
			left:  []float64{math.MaxFloat64, 1, math.MaxFloat64},
			right: []float64{math.MaxFloat64, 1, -math.MaxFloat64}, want: vectorstore.ScoreFromInnerProduct(1),
		},
	} {
		t.Run(name, func(t *testing.T) {
			got := inmemory.DotProductSimilarity(testCase.left, testCase.right)
			if got != testCase.want {
				t.Fatalf("score = %g, want %g", got, testCase.want)
			}
		})
	}
}

func TestStoreSearchPreservesCosineAcrossEmbeddingScales(t *testing.T) {
	for name, scale := range map[string]float64{"ordinary": 1, "large": 1e200, "small": 1e-200} {
		t.Run(name, func(t *testing.T) {
			model := embedding.ModelFunc(func(_ context.Context, request *embedding.Request) (*embedding.Response, error) {
				outputs := make([]*embedding.Output, len(request.Texts))
				for index := range outputs {
					outputs[index] = &embedding.Output{Embedding: []float64{scale, scale}}
				}
				return embedding.NewResponse(outputs, nil)
			})
			store, err := inmemory.NewStore(inmemory.StoreConfig{EmbeddingModel: model})
			if err != nil {
				t.Fatal(err)
			}
			indexDocuments(t, store, mustDoc(t, "self", "same", nil))
			got, err := search(store, t.Context(), &vectorstore.SearchRequest{
				Query: "same", Options: vectorstore.SearchOptions{MinScore: 0.9},
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 1 || got[0].Document.ID != "self" {
				t.Fatalf("self search returned %v, want the indexed document", got)
			}
		})
	}
}

func TestSimilarityRejectsNonFiniteComponents(t *testing.T) {
	for name, similarity := range map[string]inmemory.Similarity{
		"cosine": inmemory.CosineSimilarity, "dot product": inmemory.DotProductSimilarity, "euclidean": inmemory.EuclideanSimilarity,
	} {
		t.Run(name, func(t *testing.T) {
			for _, value := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
				invalid, zero := []float64{value}, []float64{0}
				if got := similarity(invalid, zero); got.Validate() == nil {
					t.Errorf("non-finite left component %g scored %g", value, got)
				}
				if got := similarity(zero, invalid); got.Validate() == nil {
					t.Errorf("non-finite right component %g scored %g", value, got)
				}
			}
		})
	}
}

func BenchmarkDotProductSimilarity(b *testing.B) {
	left, right := make([]float64, 1536), make([]float64, 1536)
	for index := range left {
		left[index] = float64(index%17-8) / 256
		right[index] = float64(index%13-6) / 256
	}
	b.ReportAllocs()
	for b.Loop() {
		inmemory.DotProductSimilarity(left, right)
	}
}
