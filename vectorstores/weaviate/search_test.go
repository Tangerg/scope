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

type testBatcher struct{}

func (t testBatcher) Batch(_ context.Context, documents []*document.Document) ([][]*document.Document, error) {
	return [][]*document.Document{documents}, nil
}

func TestNativeMetricNormalizesOnlyThroughCore(t *testing.T) {
	for _, sample := range []struct {
		metric string
		raw    float64
		want   vectorstore.Score
	}{{distanceCosine, 0, 1}, {distanceDot, 0, .5}, {distanceL2Squared, 1, .5}, {distanceHamming, 1, .5}, {distanceManhattan, 1, .5}} {
		score, err := (nativeSchema{metric: sample.metric}).score(sample.raw, vectorstore.SearchModeSemantic)
		if err != nil || score != sample.want {
			t.Fatalf("score=%v error=%v", score, err)
		}
	}
}

func TestHybridPolicyRejectsNonfiniteAndOutOfRangeValues(t *testing.T) {
	for _, alpha := range []float32{float32(math.NaN()), float32(math.Inf(1)), -.1, 1.01} {
		config := StoreConfig{Client: new(weaviateclient.Client), ClassName: "Documents", HybridAlpha: &alpha, DocumentBatcher: testBatcher{}, EmbeddingModel: embedding.ModelFunc(func(context.Context, *embedding.Request) (*embedding.Response, error) { return nil, nil })}
		if err := config.Validate(); err == nil {
			t.Fatal("invalid hybrid policy was accepted")
		}
	}
}
