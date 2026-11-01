package weaviate

import (
	"math"
	"testing"

	"github.com/Tangerg/scope/core/vectorstore"
)

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
		config := StoreConfig{Client: new(Client), Collection: "Documents", HybridAlpha: &alpha, Space: fixtureSpace}
		if err := config.Validate(); err == nil {
			t.Fatal("invalid hybrid policy was accepted")
		}
	}
}
