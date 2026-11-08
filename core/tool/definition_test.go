package tool_test

import (
	"bytes"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"testing"

	"github.com/Tangerg/scope/core/jsonschema"
	"github.com/Tangerg/scope/core/tool"
)

func TestDefinitionRejectsInvalidSchemaAtEveryBoundary(t *testing.T) {
	const objectSchema = `{"type":"object"}`
	tests := []struct {
		name   string
		input  string
		output string
	}{
		{name: "invalid property type", input: `{"type":"object","properties":{"x":{"type":"nonsense"}}}`},
		{name: "invalid keyword value", input: `{"type":"object","required":"x"}`},
		{name: "unresolved local reference", input: `{"type":"object","properties":{"x":{"$ref":"#/$defs/missing"}}}`},
		{name: "external reference", input: `{"type":"object","properties":{"x":{"$ref":"https://example.invalid/schema"}}}`},
		{name: "invalid output type", input: objectSchema, output: `{"type":"nonsense"}`},
		{name: "invalid output property", input: objectSchema, output: `{"type":"object","properties":{"x":{"minimum":"zero"}}}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			definition := tool.Definition{Name: "lookup", InputSchema: json.RawMessage(test.input)}
			if test.output != "" {
				definition.OutputSchema = json.RawMessage(test.output)
			}
			if err := definition.Validate(); !errors.Is(err, tool.ErrInvalidDefinition) || !errors.Is(err, jsonschema.ErrInvalid) {
				t.Fatalf("Validate error = %v, want invalid definition and schema", err)
			}
			if _, err := jsonv2.Marshal(definition); !errors.Is(err, tool.ErrInvalidDefinition) {
				t.Fatalf("Marshal error = %v, want invalid definition", err)
			}
			if _, err := tool.Bind(definitionTool{definition: definition}); !errors.Is(err, tool.ErrInvalidTool) || !errors.Is(err, tool.ErrInvalidDefinition) || !errors.Is(err, jsonschema.ErrInvalid) {
				t.Fatalf("Bind error = %v, want invalid tool, definition and schema", err)
			}

			wire, err := jsonv2.Marshal(struct {
				Name   string          `json:"name"`
				Input  json.RawMessage `json:"input_schema"`
				Output json.RawMessage `json:"output_schema,omitzero"`
			}{Name: definition.Name, Input: definition.InputSchema, Output: definition.OutputSchema})
			if err != nil {
				t.Fatal(err)
			}
			original := tool.Definition{Name: "original", InputSchema: json.RawMessage(objectSchema)}
			decoded := original.Clone()
			if err := jsonv2.Unmarshal(wire, &decoded); !errors.Is(err, tool.ErrInvalidDefinition) {
				t.Fatalf("Unmarshal error = %v, want invalid definition", err)
			}
			if decoded.Name != original.Name || !bytes.Equal(decoded.InputSchema, original.InputSchema) || len(decoded.OutputSchema) != 0 {
				t.Fatalf("invalid definition changed receiver: %+v", decoded)
			}
		})
	}
}

func TestDefinitionRejectsPresentEmptyOutputSchema(t *testing.T) {
	definition := tool.Definition{Name: "lookup", InputSchema: json.RawMessage(`{"type":"object"}`), OutputSchema: json.RawMessage{}}
	if err := definition.Validate(); !errors.Is(err, tool.ErrInvalidDefinition) || !errors.Is(err, jsonschema.ErrInvalid) {
		t.Fatalf("Validate error = %v, want invalid definition and schema", err)
	}
}

func TestDefinitionAcceptsSelfContainedSchemaContracts(t *testing.T) {
	definition := tool.Definition{
		Name:         "lookup",
		InputSchema:  json.RawMessage(`{"type":"object","$defs":{"identifier":{"type":"integer","minimum":9007199254740993}},"properties":{"id":{"$ref":"#/$defs/identifier"}},"required":["id"],"additionalProperties":false}`),
		OutputSchema: json.RawMessage("true"),
	}
	if err := definition.Validate(); err != nil {
		t.Fatal(err)
	}
	binding, err := tool.Bind(definitionTool{definition: definition})
	if err != nil {
		t.Fatal(err)
	}
	for _, arguments := range []string{`{"id":9007199254740993}`, `{"id":9223372036854775807}`} {
		if _, err := binding.Contract().Prepare(tool.Request{Name: definition.Name, Arguments: arguments}, tool.Admission{}); err != nil {
			t.Fatalf("Prepare(%s): %v", arguments, err)
		}
	}
	if _, err := binding.Contract().Prepare(tool.Request{Name: definition.Name, Arguments: `{"id":9007199254740992}`}, tool.Admission{}); !errors.Is(err, tool.ErrInvalidInvocation) {
		t.Fatalf("Prepare rounded lower bound error = %v", err)
	}
}
