package rerank_test

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"math"
	"testing"

	"github.com/Tangerg/scope/core/rerank"
)

func TestScorePreservesFiniteModelScale(t *testing.T) {
	for _, value := range []float64{-math.MaxFloat64, -1, -0.25, 0, 1, 12.5, math.MaxFloat64} {
		score := rerank.Score(value)
		if err := score.Validate(); err != nil {
			t.Fatalf("finite score %v rejected: %v", value, err)
		}
		encoded, err := jsonv2.Marshal(score)
		if err != nil {
			t.Fatal(err)
		}
		var decoded rerank.Score
		if err := jsonv2.Unmarshal(encoded, &decoded); err != nil || decoded.Float64() != value {
			t.Fatalf("score %v round trip = %v, %v", value, decoded, err)
		}
	}
	for _, value := range []float64{math.NaN(), math.Inf(-1), math.Inf(1)} {
		score := rerank.Score(value)
		if err := score.Validate(); !errors.Is(err, rerank.ErrInvalidResponse) {
			t.Fatalf("non-finite score %v validation = %v", value, err)
		}
		if _, err := jsonv2.Marshal(score); !errors.Is(err, rerank.ErrInvalidResponse) {
			t.Fatalf("non-finite score %v marshal = %v", value, err)
		}
	}
}

func TestResponseOrdersNativeScoresWithoutNormalization(t *testing.T) {
	request, err := rerank.NewRequest("query", []string{"first", "second", "third"})
	if err != nil {
		t.Fatal(err)
	}
	response := rerank.Response{Results: []*rerank.Result{
		{Index: 2, Score: 12.5}, {Index: 0, Score: -0.25}, {Index: 1, Score: -2},
	}}
	if err := response.ValidateFor(request); err != nil {
		t.Fatal(err)
	}
	response.Results[1], response.Results[2] = response.Results[2], response.Results[1]
	if err := response.ValidateFor(request); !errors.Is(err, rerank.ErrInvalidResponse) {
		t.Fatalf("ascending native scores accepted: %v", err)
	}
}
