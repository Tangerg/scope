package jsonschema_test

import (
	"errors"
	"testing"

	"github.com/Tangerg/scope/core/jsonschema"
)

func TestForRejectsTagsThatReplaceOwnedWireShapes(t *testing.T) {
	for name, derive := range map[string]func() (jsonschema.Schema, error){
		"byte array": jsonschema.For[struct {
			Data [2]byte `json:"data" jsonschema:"type=number"`
		}],
		"float": jsonschema.For[struct {
			Value float32 `json:"value" jsonschema:"type=string"`
		}],
		"duplicate type owner": jsonschema.For[struct {
			Value string `json:"value" jsonschema:"type=string"`
		}],
		"oneof type": jsonschema.For[struct {
			Value string `json:"value" jsonschema:"oneof_type=string;null"`
		}],
		"anyof type": jsonschema.For[struct {
			Value string `json:"value" jsonschema:"anyof_type=string;null"`
		}],
		"oneof reference": jsonschema.For[struct {
			Value string `json:"value" jsonschema:"oneof_ref=#"`
		}],
		"anyof reference": jsonschema.For[struct {
			Value []string `json:"value" jsonschema:"anyof_ref=#"`
		}],
		"nested": jsonschema.For[struct {
			Child struct {
				Value string `json:"value" jsonschema:"type=string"`
			} `json:"child"`
		}],
	} {
		t.Run(name, func(t *testing.T) {
			shape, err := derive()
			if shape.Valid() || !errors.Is(err, jsonschema.ErrInvalid) || !errors.Is(err, jsonschema.ErrUnsupportedFieldLayout) {
				t.Fatalf("shape replacement was not rejected at derivation: %s, %v", shape.JSON(), err)
			}
		})
	}
}

func TestForPreservesOrdinarySchemaRefinements(t *testing.T) {
	type input struct {
		Value string `json:"value" jsonschema:"description=first\\,type=number,minLength=2"`
	}
	shape, err := jsonschema.For[input]()
	if err != nil {
		t.Fatal(err)
	}
	if err = shape.Validate([]byte(`{"value":"valid"}`)); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{`{"value":"x"}`, `{"value":12}`} {
		if err = shape.Validate([]byte(raw)); !errors.Is(err, jsonschema.ErrValidation) {
			t.Errorf("ordinary tag changed its constraint for %s: %v", raw, err)
		}
	}
}
