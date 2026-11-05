package embedding_test

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"math"
	"slices"
	"testing"

	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/vector"
)

func TestOutputPreservesCoordinateFailureIdentity(t *testing.T) {
	for _, values := range [][]float64{nil, {math.Inf(1)}, {math.NaN()}} {
		output, err := embedding.NewOutput(values, nil)
		if output != nil || !errors.Is(err, embedding.ErrInvalidResponse) || !errors.Is(err, vector.ErrInvalidCoordinates) {
			t.Fatalf("output=%v error=%v; want domain and coordinate failure identities", output, err)
		}
	}
	for _, raw := range []string{`{"embedding":[1,null]}`, `{"embedding":[]}`, `{"embedding":null}`} {
		output := embedding.Output{Embedding: vector.Coordinates[float64]{3, 4}}
		err := jsonv2.Unmarshal([]byte(raw), &output)
		if !errors.Is(err, embedding.ErrInvalidResponse) || !errors.Is(err, vector.ErrInvalidCoordinates) || !slices.Equal(output.Embedding, []float64{3, 4}) {
			t.Fatalf("decode %s: output=%v error=%v; want unchanged receiver and both failure identities", raw, output, err)
		}
	}
	var output embedding.Output
	if err := jsonv2.Unmarshal([]byte(`{"embedding":[0,1]}`), &output); err != nil || !slices.Equal(output.Embedding, []float64{0, 1}) {
		t.Fatalf("explicit zero = %v, %v", output, err)
	}
}
