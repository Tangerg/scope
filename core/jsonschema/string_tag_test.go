package jsonschema_test

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/jsonschema"
)

type quotedInteger struct {
	Value int8 `json:"value,string"`
}

type recursiveQuotedInteger struct {
	Next  *recursiveQuotedInteger `json:"next,omitzero"`
	Value uint64                  `json:"value,string"`
}

func TestForRejectsStringTagsWithoutAnExplicitWireOwner(t *testing.T) {
	for name, derive := range map[string]func() (jsonschema.Schema, error){
		"integer": jsonschema.For[quotedInteger],
		"unsigned": jsonschema.For[struct {
			Value uint64 `json:"value,string"`
		}],
		"float": jsonschema.For[struct {
			Value float64 `json:"value,string"`
		}],
		"pointer": jsonschema.For[struct {
			Value *int `json:"value,string"`
		}],
		"boolean": jsonschema.For[struct {
			Value bool `json:"value,string"`
		}],
		"text": jsonschema.For[struct {
			Value string `json:"value,string"`
		}],
		"composite": jsonschema.For[struct {
			Value []int `json:"value,string"` //nolint:staticcheck // This invalid tag is the input whose rejection the test proves.
		}],
		"nested":    jsonschema.For[map[string][]quotedInteger],
		"recursive": jsonschema.For[recursiveQuotedInteger],
	} {
		t.Run(name, func(t *testing.T) {
			schema, err := derive()
			if schema.Valid() || !errors.Is(err, jsonschema.ErrInvalid) || !errors.Is(err, jsonschema.ErrUnsupportedStringTag) || !strings.Contains(err.Error(), ".Value") {
				t.Fatalf("unsupported field established a schema: valid=%t error=%v", schema.Valid(), err)
			}
		})
	}
}

func TestForRetainsExplicitStringAndBooleanWireOwners(t *testing.T) {
	type contract struct {
		Text explicitlyEncodedText `json:"text,string"` //nolint:staticcheck // The custom JSON v2 codec owns this wire representation.
		Flag explicitlyEncodedFlag `json:"flag,string"` //nolint:staticcheck // The custom JSON v2 codec owns this wire representation.
	}
	schema, err := jsonschema.For[contract]()
	if err != nil {
		t.Fatal(err)
	}
	wire, err := jsonv2.Marshal(contract{})
	if err != nil || string(wire) != `{"text":"owned text","flag":true}` {
		t.Fatalf("explicit wire=%s error=%v", wire, err)
	}
	if err := schema.Validate(wire); err != nil {
		t.Fatalf("explicit codec and wire model lost their representation: %v", err)
	}
}

type explicitlyEncodedText struct{}

func (explicitlyEncodedText) MarshalJSON() ([]byte, error) { return []byte(`"owned text"`), nil }

func (explicitlyEncodedText) JSONSchemaModel() any { return "" }

type explicitlyEncodedFlag struct{}

func (explicitlyEncodedFlag) MarshalJSON() ([]byte, error) { return []byte(`true`), nil }

func (explicitlyEncodedFlag) JSONSchemaModel() any { return false }
