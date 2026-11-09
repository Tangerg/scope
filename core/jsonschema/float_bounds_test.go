package jsonschema_test

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"math"
	"testing"

	"github.com/Tangerg/scope/core/jsonschema"
)

func assertFloatWireRange[T ~float32 | ~float64](t *testing.T, valid, invalid []string) {
	t.Helper()
	schema, err := jsonschema.For[T]()
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range valid {
		var value T
		if err = jsonv2.Unmarshal([]byte(raw), &value); err != nil {
			t.Fatalf("valid fixture %s cannot decode: %v", raw, err)
		}
		if err = schema.Validate([]byte(raw)); err != nil {
			t.Errorf("schema rejected finite %s: %v", raw, err)
		}
	}
	for _, raw := range invalid {
		var value T
		if err = jsonv2.Unmarshal([]byte(raw), &value); err == nil {
			t.Fatalf("invalid fixture %s decoded successfully", raw)
		}
		if err = schema.Validate([]byte(raw)); !errors.Is(err, jsonschema.ErrValidation) {
			t.Errorf("schema accepted overflow %s: %v", raw, err)
		}
	}
}

func TestFloatSchemaPreservesTheFiniteRoundingRange(t *testing.T) {
	t.Run("float32", func(t *testing.T) {
		assertFloatWireRange[float32](t,
			[]string{"0", "-0", "1e-1000", "3.4028235e38", "-3.4028235e38", "340282356779733661637539395458142568447"},
			[]string{"1e100", "-1e100", "340282356779733661637539395458142568448", "-340282356779733661637539395458142568448"})
		assertSchemaAcceptsWire(t, struct{ Number float32 }{math.MaxFloat32})
	})
	t.Run("float64", func(t *testing.T) {
		assertFloatWireRange[float64](t,
			[]string{"0", "1e-1000", "1.7976931348623157e308", "-1.7976931348623157e308"},
			[]string{"1e309", "-1e309", "1.7976931348623159e308"})
		assertSchemaAcceptsWire(t, struct{ Number float64 }{math.MaxFloat64})
	})
}

type constrainedFloat32 float32

func (constrainedFloat32) JSONSchemaConstraints() jsonschema.Constraints {
	return jsonschema.Constraints{Minimum: new(float64(0))}
}

func TestFloatBoundsIntersectOwnedConstraintsAndFieldTags(t *testing.T) {
	type input struct {
		Value constrainedFloat32 `json:"value" jsonschema:"minimum=-1e200,maximum=1e200"`
	}
	schema, err := jsonschema.For[input]()
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{`{"value":-1}`, `{"value":1e100}`} {
		if err = schema.Validate([]byte(raw)); !errors.Is(err, jsonschema.ErrValidation) {
			t.Errorf("accepted %s: %v", raw, err)
		}
	}
	for _, raw := range []string{`{"value":0}`, `{"value":3.4028235e38}`} {
		if err = schema.Validate([]byte(raw)); err != nil {
			t.Errorf("rejected %s: %v", raw, err)
		}
	}
}
