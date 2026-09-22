package chat_test

import (
	"encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"reflect"
	"testing"

	"github.com/Tangerg/scope/core/chat"
)

func TestOutputFormatConstructorsAndJSON(t *testing.T) {
	for _, formatType := range []chat.OutputFormatType{chat.OutputFormatText, chat.OutputFormatJSON} {
		format, err := chat.NewOutputFormat(formatType)
		if err != nil {
			t.Fatalf("NewOutputFormat(%q): %v", formatType, err)
		}
		encoded, err := jsonv2.Marshal(format)
		if err != nil {
			t.Fatal(err)
		}
		var decoded chat.OutputFormat
		if err := jsonv2.Unmarshal(encoded, &decoded); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(decoded, format) {
			t.Fatalf("round trip = %#v, want %#v", decoded, format)
		}
	}

	schema := json.RawMessage(`{"type":"object","properties":{"answer":{"type":"string"}}}`)
	format, err := chat.NewJSONSchemaOutputFormat(chat.JSONSchemaConfig{
		Name: "answer", Description: "The answer payload", Schema: schema,
	})
	if err != nil {
		t.Fatal(err)
	}
	schema[0] = '['
	if format.Schema[0] != '{' {
		t.Fatal("NewJSONSchemaOutputFormat retained caller-owned schema bytes")
	}
	if err := format.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestOutputFormatSchemaAs(t *testing.T) {
	format, err := chat.NewJSONSchemaOutputFormat(chat.JSONSchemaConfig{
		Name: "answer", Schema: json.RawMessage(`{"type":"object","properties":{"answer":{"type":"string"}}}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	schema, err := format.SchemaAs[map[string]any]()
	if err != nil {
		t.Fatal(err)
	}
	if schema["type"] != "object" {
		t.Fatalf("schema type = %#v, want object", schema["type"])
	}

	text, err := chat.NewOutputFormat(chat.OutputFormatText)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := text.SchemaAs[any](); !errors.Is(err, chat.ErrInvalidOutputFormat) {
		t.Fatalf("text SchemaAs = %v, want ErrInvalidOutputFormat", err)
	}
	var nilFormat *chat.OutputFormat
	if _, err := nilFormat.SchemaAs[any](); !errors.Is(err, chat.ErrInvalidOutputFormat) {
		t.Fatalf("nil SchemaAs = %v, want ErrInvalidOutputFormat", err)
	}
}

func TestOutputFormatRejectsInvalidContracts(t *testing.T) {
	tests := []chat.OutputFormat{
		{},
		{Type: "xml"},
		{Type: chat.OutputFormatText, Schema: json.RawMessage(`{}`)},
		{Type: chat.OutputFormatJSON, Name: "answer"},
		{Type: chat.OutputFormatJSONSchema, Schema: json.RawMessage(`{}`)},
		{Type: chat.OutputFormatJSONSchema, Name: " answer ", Schema: json.RawMessage(`{}`)},
		{Type: chat.OutputFormatJSONSchema, Name: "answer", Description: " description ", Schema: json.RawMessage(`{}`)},
		{Type: chat.OutputFormatJSONSchema, Name: "answer", Schema: json.RawMessage(`[]`)},
		{Type: chat.OutputFormatJSONSchema, Name: "answer", Schema: json.RawMessage(`{`)},
		{Type: chat.OutputFormatJSONSchema, Name: "answer", Schema: json.RawMessage(`{"type":"object","type":"array"}`)},
	}
	for _, format := range tests {
		if err := format.Validate(); !errors.Is(err, chat.ErrInvalidOutputFormat) {
			t.Errorf("Validate(%#v) = %v, want ErrInvalidOutputFormat", format, err)
		}
		if _, err := jsonv2.Marshal(format); !errors.Is(err, chat.ErrInvalidOutputFormat) {
			t.Errorf("Marshal(%#v) = %v, want ErrInvalidOutputFormat", format, err)
		}
	}
}

func TestOutputFormatCloneAndAtomicUnmarshal(t *testing.T) {
	format, err := chat.NewJSONSchemaOutputFormat(chat.JSONSchemaConfig{
		Name: "answer", Schema: json.RawMessage(`{"type":"object"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	clone := format.Clone()
	clone.Schema[0] = '['
	if format.Schema[0] != '{' {
		t.Fatal("OutputFormat.Clone aliases schema bytes")
	}

	before := format.Clone()
	if err := jsonv2.Unmarshal([]byte(`{"type":"json_schema","name":"answer","schema":[]}`), &format); !errors.Is(err, chat.ErrInvalidOutputFormat) {
		t.Fatalf("Unmarshal = %v, want ErrInvalidOutputFormat", err)
	}
	if !reflect.DeepEqual(format, *before) {
		t.Fatalf("failed Unmarshal mutated receiver: %#v", format)
	}
	if err := jsonv2.Unmarshal([]byte(`{"type":"text","type":"json"}`), &format); !errors.Is(err, jsontext.ErrDuplicateName) {
		t.Fatalf("duplicate field Unmarshal = %v, want jsontext.ErrDuplicateName", err)
	}
	if !reflect.DeepEqual(format, *before) {
		t.Fatalf("duplicate field Unmarshal mutated receiver: %#v", format)
	}

	var nilFormat *chat.OutputFormat
	if nilFormat.Clone() != nil {
		t.Fatal("nil OutputFormat.Clone must return nil")
	}
	if err := nilFormat.UnmarshalJSON([]byte(`{"type":"text"}`)); !errors.Is(err, chat.ErrInvalidOutputFormat) {
		t.Fatalf("nil receiver = %v, want ErrInvalidOutputFormat", err)
	}
}

func TestEmptyOutputSchemaRoundTrip(t *testing.T) {
	format, err := chat.NewJSONSchemaOutputFormat(chat.JSONSchemaConfig{Name: "anything", Schema: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	data, err := jsonv2.Marshal(format)
	if err != nil {
		t.Fatal(err)
	}
	var restored chat.OutputFormat
	if err := jsonv2.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}
	if string(restored.Schema) != `{}` {
		t.Fatalf("schema = %s", restored.Schema)
	}
}
