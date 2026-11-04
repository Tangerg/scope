package openai

import (
	"bytes"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"testing"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/jsonschema"
)

type boundedOutput struct {
	Count int8   `json:"count" jsonschema:"minimum=-10,maximum=10"`
	ID    uint64 `json:"id"`
	Label string `json:"label"`
}

func (boundedOutput) JSONSchemaConstraints() jsonschema.Constraints {
	return jsonschema.Constraints{Properties: map[string]jsonschema.Constraints{
		"count": {Minimum: new(float64(-5)), Maximum: new(float64(5))},
	}}
}

func TestNativeSchemaPreservesDerivedIntegerAndDomainBounds(t *testing.T) {
	canonical, err := jsonschema.For[boundedOutput]()
	if err != nil {
		t.Fatal(err)
	}
	original := canonical.JSON()
	format, err := corechat.NewJSONSchemaOutputFormat(corechat.JSONSchemaConfig{Name: "result", Schema: original})
	if err != nil {
		t.Fatal(err)
	}
	chat, err := mapChatOutputFormat(&format)
	if err != nil {
		t.Fatal(err)
	}
	responses, err := mapResponsesOutputFormat(&format)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		schema any
	}{
		{name: "chat", schema: chat.OfJSONSchema.JSONSchema.Schema},
		{name: "responses", schema: responses.OfJSONSchema.Schema},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw, err := jsonv2.Marshal(test.schema)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(raw, []byte(`"allOf"`)) || !bytes.Contains(raw, []byte(`18446744073709551615`)) {
				t.Fatalf("native schema did not preserve exact supported bounds: %s", raw)
			}
			native, err := jsonschema.Parse(raw)
			if err != nil {
				t.Fatal(err)
			}
			for _, sample := range []string{
				`{"count":-5,"id":0,"label":"ok"}`,
				`{"count":5,"id":18446744073709551615,"label":"ok"}`,
				`{"count":-6,"id":0,"label":"ok"}`,
				`{"count":6,"id":0,"label":"ok"}`,
				`{"count":1.5,"id":0,"label":"ok"}`,
				`{"count":1,"id":18446744073709551616,"label":"ok"}`,
				`{"count":1,"id":-1,"label":"ok"}`,
				`{"count":1,"id":1}`,
				`{"count":1,"id":1,"label":"ok","extra":true}`,
			} {
				want := canonical.Validate([]byte(sample)) == nil
				if got := native.Validate([]byte(sample)) == nil; got != want {
					t.Errorf("sample %s: native accepts=%t, canonical accepts=%t", sample, got, want)
				}
			}
		})
	}
	if !bytes.Equal(format.Schema, original) {
		t.Fatal("provider projection mutated the canonical schema")
	}
}

func TestNativeSchemaRejectsContractsThatCannotBeEnforced(t *testing.T) {
	for _, raw := range []string{
		`{"type":"object"}`,
		`{"type":"object","properties":{"optional":{"type":"string"}},"additionalProperties":false}`,
		`{"type":"object","properties":{"value":{"type":"string","minLength":3}},"required":["value"],"additionalProperties":false}`,
		`{"type":"object","properties":{"value":{"oneOf":[{"type":"integer"},{"type":"number"}]}},"required":["value"],"additionalProperties":false}`,
		`{"type":"object","properties":{"value":{"type":"string","format":"uri"}},"required":["value"],"additionalProperties":false}`,
		`{"type":"object","properties":{"value":{"$ref":"#/$defs/value","description":"refinement"}},"required":["value"],"additionalProperties":false,"$defs":{"value":{"type":"string"}}}`,
	} {
		t.Run(raw, func(t *testing.T) {
			format, err := corechat.NewJSONSchemaOutputFormat(corechat.JSONSchemaConfig{Name: "result", Schema: json.RawMessage(raw)})
			if err != nil {
				t.Fatal(err)
			}
			_, chatErr := mapChatOutputFormat(&format)
			_, responsesErr := mapResponsesOutputFormat(&format)
			if !errors.Is(chatErr, corechat.ErrUnsupportedOutputFormat) || !errors.Is(responsesErr, corechat.ErrUnsupportedOutputFormat) {
				t.Fatalf("chat=%v responses=%v", chatErr, responsesErr)
			}
		})
	}
}

func TestNativeSchemaLowersOnlyDisjointOneOfBranches(t *testing.T) {
	format, err := corechat.NewJSONSchemaOutputFormat(corechat.JSONSchemaConfig{
		Name: "result", Schema: json.RawMessage(`{"type":"object","properties":{"value":{"oneOf":[{"type":"string"},{"type":"null"}]}},"required":["value"],"additionalProperties":false}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	schema, err := nativeOutputSchema(&format)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := jsonv2.Marshal(schema)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(`"oneOf"`)) || !bytes.Contains(raw, []byte(`"anyOf"`)) {
		t.Fatalf("native schema = %s", raw)
	}
	native, err := jsonschema.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{`{"value":"ok"}`, `{"value":null}`} {
		if err := native.Validate([]byte(raw)); err != nil {
			t.Fatal(err)
		}
	}
	if err := native.Validate([]byte(`{}`)); err == nil {
		t.Fatal("nullable required field became optional")
	}
}

func TestNativeSchemaCanExposeASingleClosedIntersection(t *testing.T) {
	format, err := corechat.NewJSONSchemaOutputFormat(corechat.JSONSchemaConfig{
		Name: "result", Schema: json.RawMessage(`{"description":"domain projection","allOf":[{"type":"object","properties":{"value":{"type":"string"}},"required":["value"],"additionalProperties":false}]}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	schema, err := nativeOutputSchema(&format)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := jsonv2.Marshal(schema)
	if err != nil {
		t.Fatal(err)
	}
	native, err := jsonschema.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := native.Validate([]byte(`{"value":"ok"}`)); err != nil {
		t.Fatal(err)
	}
	if err := native.Validate([]byte(`{"value":"ok","extra":true}`)); err == nil {
		t.Fatal("closed object became open while lowering allOf")
	}
}
