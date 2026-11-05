package vector_test

import (
	"slices"
	"testing"

	"github.com/Tangerg/scope/core/vector"
)

func TestFloat32(t *testing.T) {
	if vector.Float32(nil) != nil {
		t.Fatal("nil vector became non-nil")
	}
	if got, want := vector.Float32([]float64{1.5, -2.25}), []float32{1.5, -2.25}; !slices.Equal(got, want) {
		t.Fatalf("Float32 = %v, want %v", got, want)
	}
}
