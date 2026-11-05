package vector_test

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"math"
	"slices"
	"testing"

	"github.com/Tangerg/scope/core/vector"
)

func TestCoordinatesRequireMeasuredNumericComponents(t *testing.T) {
	t.Run("float32", testCoordinateJSON[float32])
	t.Run("float64", testCoordinateJSON[float64])
}

func testCoordinateJSON[T float32 | float64](t *testing.T) {
	t.Helper()
	for _, raw := range []string{`null`, `[]`, `[1,null]`, `["1"]`, `[true]`, `[1e1000]`} {
		coordinates := vector.Coordinates[T]{3, 4}
		if err := jsonv2.Unmarshal([]byte(raw), &coordinates); !errors.Is(err, vector.ErrInvalidCoordinates) {
			t.Fatalf("decode %s: %v; want invalid coordinates", raw, err)
		}
		if !slices.Equal(coordinates, vector.Coordinates[T]{3, 4}) {
			t.Fatalf("failed decode changed previous coordinates: %v", coordinates)
		}
	}
	var coordinates vector.Coordinates[T]
	if err := jsonv2.Unmarshal([]byte(`[0,1.25]`), &coordinates); err != nil {
		t.Fatal(err)
	}
	raw, err := jsonv2.Marshal(coordinates)
	if err != nil || string(raw) != `[0,1.25]` {
		t.Fatalf("explicit zero round trip = %s, %v", raw, err)
	}
	for _, value := range []vector.Coordinates[T]{nil, {}, {T(math.NaN())}, {T(math.Inf(1))}} {
		if _, err := jsonv2.Marshal(value); !errors.Is(err, vector.ErrInvalidCoordinates) {
			t.Fatalf("marshal %v: %v; want invalid coordinates", value, err)
		}
	}
}
