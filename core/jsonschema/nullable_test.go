package jsonschema_test

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"testing"

	"github.com/Tangerg/scope/core/jsonschema"
)

func TestNullableAcceptsAlreadyNullableWireShapes(t *testing.T) {
	type input struct {
		Data *[]byte `json:"data" jsonschema:"nullable,nullable"`
	}
	shape, err := jsonschema.For[input]()
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []input{{}, {Data: new([]byte(nil))}, {Data: new([]byte{1, 2})}} {
		wire, err := jsonv2.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err = shape.Validate(wire); err != nil {
			t.Fatalf("nullable rejected codec output %s: %v", wire, err)
		}
	}
	for _, raw := range []string{`{"data":12}`, `{"data":"!!!!"}`, `{"data":[1,2]}`} {
		if err := shape.Validate([]byte(raw)); !errors.Is(err, jsonschema.ErrValidation) {
			t.Errorf("nullable widened the byte wire shape for %s: %v", raw, err)
		}
	}
}

type nullableBytesModel struct{}

func (nullableBytesModel) JSONSchemaModel() any { return []byte{} }

func (nullableBytesModel) JSONSchemaConstraints() jsonschema.Constraints {
	return shortBytes(nil).JSONSchemaConstraints()
}

func TestNullableRetainsWireBoundsAndOwnedConstraints(t *testing.T) {
	for name, derive := range map[string]func() (jsonschema.Schema, error){
		"constrained bytes": jsonschema.For[struct {
			Data *shortBytes `json:"data" jsonschema:"nullable"`
		}],
		"modeled bytes": jsonschema.For[struct {
			Data *nullableBytesModel `json:"data" jsonschema:"nullable"`
		}],
		"byte array": jsonschema.For[struct {
			Data *[2]byte `json:"data" jsonschema:"nullable"`
		}],
	} {
		t.Run(name, func(t *testing.T) {
			shape, err := derive()
			if err != nil {
				t.Fatal(err)
			}
			for _, raw := range []string{`{"data":null}`, `{"data":"AQI="}`} {
				if err := shape.Validate([]byte(raw)); err != nil {
					t.Errorf("valid nullable value %s: %v", raw, err)
				}
			}
			for _, raw := range []string{`{"data":"AAAAAA=="}`, `{"data":"!!!!"}`} {
				if err := shape.Validate([]byte(raw)); !errors.Is(err, jsonschema.ErrValidation) {
					t.Errorf("nullable discarded owned constraints for %s: %v", raw, err)
				}
			}
		})
	}
}

func TestNullableKeepsExclusiveRequiredGroups(t *testing.T) {
	type input struct {
		Choice *struct {
			Left  string `json:"left,omitzero" jsonschema:"oneof_required=left"`
			Right string `json:"right,omitzero" jsonschema:"oneof_required=right"`
		} `json:"choice" jsonschema:"nullable"`
	}
	shape, err := jsonschema.For[input]()
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{`{"choice":null}`, `{"choice":{"left":"value"}}`, `{"choice":{"right":"value"}}`} {
		if err := shape.Validate([]byte(raw)); err != nil {
			t.Errorf("valid exclusive choice %s: %v", raw, err)
		}
	}
	for _, raw := range []string{`{"choice":{}}`, `{"choice":{"left":"value","right":"value"}}`} {
		if err := shape.Validate([]byte(raw)); !errors.Is(err, jsonschema.ErrValidation) {
			t.Errorf("nullable lost oneOf exclusivity for %s: %v", raw, err)
		}
	}
}

func TestNullableDoesNotInterpretEscapedTagValues(t *testing.T) {
	type input struct {
		Value string `json:"value" jsonschema:"description=ordinary\\,nullable"`
	}
	shape, err := jsonschema.For[input]()
	if err != nil {
		t.Fatal(err)
	}
	if err = shape.Validate([]byte(`{"value":"valid"}`)); err != nil {
		t.Fatal(err)
	}
	if err = shape.Validate([]byte(`{"value":null}`)); !errors.Is(err, jsonschema.ErrValidation) {
		t.Fatalf("escaped description text changed the wire kind: %v", err)
	}
	var document struct {
		Properties map[string]struct {
			Description string `json:"description"`
		} `json:"properties"`
	}
	if err = jsonv2.Unmarshal(shape.JSON(), &document); err != nil {
		t.Fatal(err)
	}
	if description := document.Properties["value"].Description; description != "ordinary,nullable" {
		t.Fatalf("escaped description = %q", description)
	}
}
