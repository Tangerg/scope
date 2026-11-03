package chat_test

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	corecontent "github.com/Tangerg/scope/core/content"
	toolcontract "github.com/Tangerg/scope/core/tool"
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
	structured, err := toolcontract.NewJSONOutput(json.RawMessage(`{"ok":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if text, ok := structured.Text(); !ok || text != `{"ok":true}` {
		t.Fatalf("structured Text = %q, %v", text, ok)
	}

	mediaOutput := toolcontract.Output{Content: []corecontent.Block{{Kind: corecontent.Media, Media: mustImage(t)}}}
	if err := mediaOutput.Validate(); err != nil {
		t.Fatal(err)
	}
	if _, ok := mediaOutput.Text(); ok {
		t.Fatal("media output reported a lossless text projection")
	}

	invalid := toolcontract.Output{Content: []corecontent.Block{{Kind: corecontent.Kind(chat.PartToolCall)}}}
	if err := invalid.Validate(); err == nil {
		t.Fatal("nested tool call was accepted as Tool output content")
	}
	if _, err := toolcontract.NewJSONOutput(json.RawMessage(`{`)); err == nil {
		t.Fatal("malformed structured details were accepted")
	}
}

func TestJSONToolOutputRequiresDocument(t *testing.T) {
	for _, raw := range []json.RawMessage{nil, {}, []byte(" ")} {
		if _, err := toolcontract.NewJSONOutput(raw); !errors.Is(err, toolcontract.ErrInvalidOutput) {
			t.Errorf("NewJSONToolOutput(%q) error = %v", raw, err)
		}
	}
	output, err := toolcontract.NewJSONOutput([]byte(`null`))
	if err != nil || string(output.Details) != "null" {
		t.Fatalf("null output = %#v, error = %v", output, err)
	}
	if err := (toolcontract.Output{}).Validate(); err != nil {
		t.Fatalf("zero output: %v", err)
	}
}

func TestToolOutputJSONOwnsValidation(t *testing.T) {
	original, err := toolcontract.NewJSONOutput(json.RawMessage(`{"ok":true}`))
	if err != nil {
		t.Fatal(err)
	}
	data, err := jsonv2.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	var decoded toolcontract.Output
	if err := jsonv2.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if text, ok := decoded.Text(); !ok || text != `{"ok":true}` {
		t.Fatalf("round-trip Text = %q, %v", text, ok)
	}

	invalid := toolcontract.Output{Content: []corecontent.Block{{Kind: corecontent.Kind(chat.PartToolCall)}}}
	if _, err := jsonv2.Marshal(invalid); !errors.Is(err, toolcontract.ErrInvalidOutput) {
		t.Fatalf("Marshal error = %v, want ErrInvalidToolOutput", err)
	}

	decoded = original
	malformed := []byte(`{"content":[{"kind":"tool_call","tool_call":{"id":"call","name":"tool"}}]}`)
	if err := jsonv2.Unmarshal(malformed, &decoded); !errors.Is(err, toolcontract.ErrInvalidOutput) {
		t.Fatalf("Unmarshal error = %v, want ErrInvalidToolOutput", err)
	}
	if text, ok := decoded.Text(); !ok || text != `{"ok":true}` {
		t.Fatalf("failed Unmarshal mutated receiver: Text = %q, %v", text, ok)
	}

	var nilOutput *toolcontract.Output
	if err := nilOutput.UnmarshalJSON([]byte(`{}`)); !errors.Is(err, toolcontract.ErrInvalidOutput) {
		t.Fatalf("nil receiver error = %v, want ErrInvalidToolOutput", err)
	}
}

func TestToolOutputPreservesEmptyJSONValues(t *testing.T) {
	for _, raw := range []string{`null`, `""`, `{}`, `[]`, `false`, `0`} {
		t.Run(raw, func(t *testing.T) {
			output, err := toolcontract.NewJSONOutput(json.RawMessage(raw))
			if err != nil {
				t.Fatal(err)
			}
			data, err := jsonv2.Marshal(output)
			if err != nil {
				t.Fatal(err)
			}
			var restored toolcontract.Output
			if err := jsonv2.Unmarshal(data, &restored); err != nil {
				t.Fatal(err)
			}
			if string(restored.Details) != raw {
				t.Fatalf("details = %s, want %s; wire = %s", restored.Details, raw, data)
			}
		})
	}
}

func TestToolResultMarkedOutputCarriesTheErrorFlagInBand(t *testing.T) {
	text := toolcontract.NewTextOutput("file not found")
	structured, err := toolcontract.NewJSONOutput(json.RawMessage(`{"code":404}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name    string
		result  chat.ToolResult
		want    string
		content int
	}{
		{name: "success is unchanged", result: chat.ToolResult{Output: text}, want: "file not found", content: 1},
		{name: "structured success is unchanged", result: chat.ToolResult{Output: structured}, want: `{"code":404}`, content: 0},
		{name: "text error", result: chat.ToolResult{Output: text, IsError: true}, want: "error: file not found", content: 2},
		{name: "structured error", result: chat.ToolResult{Output: structured, IsError: true}, want: `error: {"code":404}`, content: 2},
		{name: "empty error", result: chat.ToolResult{IsError: true}, want: "error: ", content: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			marked := test.result.MarkedOutput()
			got, ok := marked.Text()
			if !ok || got != test.want || len(marked.Content) != test.content || marked.Validate() != nil {
				t.Fatalf("MarkedOutput() = %+v (%q, %v)", marked, got, ok)
			}
		})
	}
	media := chat.ToolResult{IsError: true, Output: toolcontract.Output{Content: []corecontent.Block{{Kind: corecontent.Text, Text: "see image"}}}}
	if marked := media.MarkedOutput(); marked.Content[0].Text != "error: " || media.Output.Content[0].Text != "see image" {
		t.Fatal("marking mutated the result or lost the marker")
	}
}
