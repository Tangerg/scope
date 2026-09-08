package weaviate

import (
	"context"
	"math"
	"testing"

	weaviateclient "github.com/weaviate/weaviate-go-client/v5/weaviate"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/vectorstore"
)

func TestDistanceMetricContract(t *testing.T) {
	tests := []struct {
		metric   DistanceMetric
		distance float64
		want     float64
	}{
		{metric: DistanceCosine, distance: 0, want: 1},
		{metric: DistanceDot, distance: 0, want: 0.5},
		{metric: DistanceL2Squared, distance: 1, want: 0.5},
		{metric: DistanceHamming, distance: 1, want: 0.5},
		{metric: DistanceManhattan, distance: 1, want: 0.5},
	}
	for _, test := range tests {
		if !test.metric.Valid() || test.metric.String() == "" {
			t.Fatalf("metric %q is not self-describing", test.metric)
		}
		if got := test.metric.score(test.distance).Float64(); math.Abs(got-test.want) > 1e-12 {
			t.Fatalf("%s score(%v) = %v, want %v", test.metric, test.distance, got, test.want)
		}
	}
	if DistanceMetric("invalid").Valid() {
		t.Fatal("Valid accepted an unknown metric")
	}
}

type testBatcher struct{}

func (testBatcher) Batch(_ context.Context, documents []*document.Document) ([][]*document.Document, error) {
	return [][]*document.Document{documents}, nil
}

func TestResultScoreUsesProviderRelevanceForEachMode(t *testing.T) {
	t.Parallel()

	store := &Store{distanceMetric: DistanceCosine}
	semantic, err := store.resultScore(map[string]any{additionalDistance: float64(0)}, vectorstore.SearchModeSemantic)
	if err != nil || semantic != 1 {
		t.Fatalf("semantic score = %v, %v", semantic, err)
	}
	hybrid, err := store.resultScore(map[string]any{additionalScore: "0.75"}, vectorstore.SearchModeHybrid)
	if err != nil || hybrid != 0.75 {
		t.Fatalf("hybrid string score = %v, %v", hybrid, err)
	}
	hybrid, err = store.resultScore(map[string]any{additionalScore: float64(0.5)}, vectorstore.SearchModeHybrid)
	if err != nil || hybrid != 0.5 {
		t.Fatalf("hybrid numeric score = %v, %v", hybrid, err)
	}
	if _, err := store.resultScore(map[string]any{additionalScore: "invalid"}, vectorstore.SearchModeHybrid); err == nil {
		t.Fatal("invalid hybrid score error = nil")
	}
	if _, err := store.resultScore(map[string]any{}, vectorstore.SearchModeSemantic); err == nil {
		t.Fatal("missing semantic distance error = nil")
	}
}

func TestStoreConfigRejectsInvalidHybridAlpha(t *testing.T) {
	t.Parallel()

	alpha := float32(1.01)
	config := StoreConfig{
		Client: new(weaviateclient.Client), ClassName: "Documents",
		EmbeddingModel: embedding.ModelFunc(func(context.Context, *embedding.Request) (*embedding.Response, error) {
			t.Fatal("configuration validation invoked the embedding model")
			return nil, nil
		}),
		DocumentBatcher: testBatcher{}, HybridAlpha: &alpha,
	}
	if err := config.Validate(); err == nil {
		t.Fatal("Validate() error = nil")
	}
}
