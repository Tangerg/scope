package chat_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/Tangerg/scope/core/chat"
)

func TestToolCallValidate(t *testing.T) {
	tests := []struct {
		name string
		call chat.ToolCall
		ok   bool
	}{
		{name: "valid", call: validToolCall(), ok: true},
		{name: "empty arguments", call: chat.ToolCall{ID: "call", Name: "tool"}, ok: true},
		{name: "malformed arguments", call: chat.ToolCall{ID: "call", Name: "tool", Arguments: `{`}, ok: true},
		{name: "empty ID", call: chat.ToolCall{Name: "tool", Arguments: `{}`}},
		{name: "empty name", call: chat.ToolCall{ID: "call", Arguments: `{}`}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.call.Validate()
			if tt.ok && err != nil {
				t.Fatalf("Validate: %v", err)
			}
			if !tt.ok && !errors.Is(err, chat.ErrInvalidToolCall) {
				t.Fatalf("Validate error = %v, want ErrInvalidToolCall", err)
			}
		})
	}
}

func TestToolResultValidate(t *testing.T) {
	if err := validToolResult().Validate(); err != nil {
		t.Fatalf("valid result: %v", err)
	}
	if err := (chat.ToolResult{Name: "tool"}).Validate(); !errors.Is(err, chat.ErrInvalidToolResult) {
		t.Fatalf("empty ID error = %v", err)
	}
	if err := (chat.ToolResult{ID: "call"}).Validate(); !errors.Is(err, chat.ErrInvalidToolResult) {
		t.Fatalf("empty name error = %v", err)
	}
	if err := (chat.ToolResult{ID: "call", Name: "tool"}).Validate(); err != nil {
		t.Fatalf("empty result must be valid: %v", err)
	}
}

func TestToolOutputPreservesStructuredAndMediaContent(t *testing.T) {
	structured, err := chat.NewJSONToolOutput(json.RawMessage(`{"ok":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if text, ok := structured.Text(); !ok || text != `{"ok":true}` {
		t.Fatalf("structured Text = %q, %v", text, ok)
	}

	mediaOutput := chat.ToolOutput{Content: []chat.ToolContent{{Kind: chat.PartMedia, Media: mustImage(t)}}}
	if err := mediaOutput.Validate(); err != nil {
		t.Fatal(err)
	}
	if _, ok := mediaOutput.Text(); ok {
		t.Fatal("media output reported a lossless text projection")
	}

	invalid := chat.ToolOutput{Content: []chat.ToolContent{{Kind: chat.PartToolCall}}}
	if err := invalid.Validate(); err == nil {
		t.Fatal("nested tool call was accepted as Tool output content")
	}
	if _, err := chat.NewJSONToolOutput(json.RawMessage(`{`)); err == nil {
		t.Fatal("malformed structured details were accepted")
	}
}

func TestJSONToolOutputRequiresDocument(t *testing.T) {
	for _, raw := range []json.RawMessage{nil, {}, []byte(" ")} {
		if _, err := chat.NewJSONToolOutput(raw); !errors.Is(err, chat.ErrInvalidToolOutput) {
			t.Errorf("NewJSONToolOutput(%q) error = %v", raw, err)
		}
	}
	output, err := chat.NewJSONToolOutput([]byte(`null`))
	if err != nil || string(output.Details) != "null" {
		t.Fatalf("null output = %#v, error = %v", output, err)
	}
	if err := (chat.ToolOutput{}).Validate(); err != nil {
		t.Fatalf("zero output: %v", err)
	}
}

func TestToolOutputJSONOwnsValidation(t *testing.T) {
	original, err := chat.NewJSONToolOutput(json.RawMessage(`{"ok":true}`))
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	var decoded chat.ToolOutput
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if text, ok := decoded.Text(); !ok || text != `{"ok":true}` {
		t.Fatalf("round-trip Text = %q, %v", text, ok)
	}

	invalid := chat.ToolOutput{Content: []chat.ToolContent{{Kind: chat.PartToolCall}}}
	if _, err := json.Marshal(invalid); !errors.Is(err, chat.ErrInvalidToolOutput) {
		t.Fatalf("Marshal error = %v, want ErrInvalidToolOutput", err)
	}

	decoded = original
	malformed := []byte(`{"content":[{"kind":"tool_call","tool_call":{"id":"call","name":"tool"}}]}`)
	if err := json.Unmarshal(malformed, &decoded); !errors.Is(err, chat.ErrInvalidToolOutput) {
		t.Fatalf("Unmarshal error = %v, want ErrInvalidToolOutput", err)
	}
	if text, ok := decoded.Text(); !ok || text != `{"ok":true}` {
		t.Fatalf("failed Unmarshal mutated receiver: Text = %q, %v", text, ok)
	}

	var nilOutput *chat.ToolOutput
	if err := nilOutput.UnmarshalJSON([]byte(`{}`)); !errors.Is(err, chat.ErrInvalidToolOutput) {
		t.Fatalf("nil receiver error = %v, want ErrInvalidToolOutput", err)
	}
}
