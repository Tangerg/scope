package chat_test

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/chat"
)

func TestToolDefinitionValidateAndRoundTrip(t *testing.T) {
	for name, description := range map[string]string{
		"plain":      "look up weather",
		"empty":      "",
		"whitespace": " \t\n",
		"untrimmed":  " look up weather ",
		"unicode":    "查找天气 ☀️",
		"long":       strings.Repeat("a", 4097),
	} {
		t.Run(name, func(t *testing.T) {
			definition := validToolDefinition()
			definition.Description = description
			if err := definition.Validate(); err != nil {
				t.Fatalf("Validate: %v", err)
			}
			encoded, err := jsonv2.Marshal(definition)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			var got chat.ToolDefinition
			if err := jsonv2.Unmarshal(encoded, &got); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			if !reflect.DeepEqual(got, definition) {
				t.Fatalf("round trip = %#v, want %#v", got, definition)
			}
		})
	}
}

func TestToolDefinitionClone(t *testing.T) {
	definition := validToolDefinition()
	wantSchema := string(definition.InputSchema)
	clone := definition.Clone()
	clone.InputSchema[0] = '['

	if got := string(definition.InputSchema); got != wantSchema {
		t.Fatalf("mutating clone changed original schema to %q", got)
	}
	if clone.Name != definition.Name || clone.Description != definition.Description {
		t.Fatalf("Clone = %+v, want scalar fields from %+v", clone, definition)
	}
}

func TestToolDefinitionRejectsInvalidValues(t *testing.T) {
	tests := []chat.ToolDefinition{
		{},
		{Name: "bad name", InputSchema: json.RawMessage(`{}`)},
		{Name: ".bad", InputSchema: json.RawMessage(`{"type":"object"}`)},
		{Name: "tool", InputSchema: json.RawMessage(`{}`)},
		{Name: "tool", InputSchema: json.RawMessage(`{"type":"array"}`)},
		{Name: "tool"},
		{Name: "tool", InputSchema: json.RawMessage(`{`)},
		{Name: "tool", InputSchema: json.RawMessage(`[]`)},
		{Name: "tool", InputSchema: json.RawMessage(`null`)},
		{Name: "tool", Description: "\xff", InputSchema: json.RawMessage(`{"type":"object"}`)},
	}
	for _, definition := range tests {
		if err := definition.Validate(); !errors.Is(err, chat.ErrInvalidToolDefinition) {
			t.Errorf("Validate(%+v) error = %v", definition, err)
		}
		if _, err := jsonv2.Marshal(definition); !errors.Is(err, chat.ErrInvalidToolDefinition) {
			t.Errorf("Marshal(%+v) error = %v", definition, err)
		}
	}
}

func TestToolDefinitionAcceptsProviderNameCharset(t *testing.T) {
	for _, name := range []string{"read", "mcp__read-1", "1_lookup", strings.Repeat("a", 64)} {
		definition := chat.ToolDefinition{Name: name, InputSchema: json.RawMessage(`{"type":"object"}`)}
		if err := definition.Validate(); err != nil {
			t.Errorf("Validate(%q): %v", name, err)
		}
	}
	tooLong := chat.ToolDefinition{Name: strings.Repeat("a", 65), InputSchema: json.RawMessage(`{"type":"object"}`)}
	if err := tooLong.Validate(); !errors.Is(err, chat.ErrInvalidToolDefinition) {
		t.Fatalf("Validate(65-byte name) error = %v, want ErrInvalidToolDefinition", err)
	}
}

func TestToolDefinitionUnmarshalIsAtomic(t *testing.T) {
	got := validToolDefinition()
	err := jsonv2.Unmarshal([]byte(`{"name":"replacement","input_schema":[]}`), &got)
	if !errors.Is(err, chat.ErrInvalidToolDefinition) {
		t.Fatalf("Unmarshal error = %v", err)
	}
	if got.Name != "weather" {
		t.Fatalf("failed Unmarshal mutated receiver: %+v", got)
	}
}

func TestToolDefinitionNilUnmarshalReceiver(t *testing.T) {
	var definition *chat.ToolDefinition
	if err := definition.UnmarshalJSON([]byte(`{}`)); !errors.Is(err, chat.ErrInvalidToolDefinition) {
		t.Fatalf("UnmarshalJSON error = %v, want ErrInvalidToolDefinition", err)
	}
}
