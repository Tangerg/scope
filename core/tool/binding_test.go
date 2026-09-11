package tool_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

type countingTool struct {
	name       string
	definition atomic.Int64
	calls      atomic.Int64
}

func (c *countingTool) Definition() chat.ToolDefinition {
	c.definition.Add(1)
	return chat.ToolDefinition{
		Name: c.name,
		InputSchema: json.RawMessage(
			`{"type":"object","properties":{"query":{"type":"string","minLength":2}},"required":["query"],"additionalProperties":false}`,
		),
	}
}

func (c *countingTool) Call(_ context.Context, invocation tool.Invocation) (chat.ToolOutput, error) {
	c.calls.Add(1)
	return chat.NewTextToolOutput(string(invocation.Arguments())), nil
}

func TestContractPromotesOnlySchemaValidCalls(t *testing.T) {
	executable := &countingTool{name: "search"}
	binding, err := tool.Bind(executable)
	if err != nil {
		t.Fatal(err)
	}
	if executable.definition.Load() != 1 {
		t.Fatalf("Definition called %d times, want exactly once", executable.definition.Load())
	}

	contract := binding.Contract()
	exposed := contract.Definition()
	executable.name = "changed"
	exposed.InputSchema[0] = '['
	if definition := contract.Definition(); definition.Name != "search" || !json.Valid(definition.InputSchema) {
		t.Fatalf("Contract retained a caller's definition mutation: %+v", definition)
	}

	for _, call := range []chat.ToolCall{
		{ID: "", Name: "search", Arguments: `{"query":"ok"}`},
		{ID: "call", Name: "other", Arguments: `{"query":"ok"}`},
		{ID: "call", Name: "search", Arguments: `{`},
		{ID: "call", Name: "search", Arguments: `{"query":"x"}`},
		{ID: "call", Name: "search", Arguments: `{"query":"ok","extra":true}`},
	} {
		if _, prepareErr := contract.Prepare(call); !errors.Is(prepareErr, tool.ErrInvalidInvocation) {
			t.Errorf("Prepare(%+v) error = %v, want ErrInvalidInvocation", call, prepareErr)
		}
	}
	if executable.calls.Load() != 0 {
		t.Fatalf("invalid promotion executed Tool %d times", executable.calls.Load())
	}

	invocation, err := contract.Prepare(chat.ToolCall{ID: "call", Name: "search", Arguments: `{"query":"scope"}`})
	if err != nil {
		t.Fatal(err)
	}
	arguments := invocation.Arguments()
	arguments[0] = '['
	if got := string(invocation.Arguments()); got != `{"query":"scope"}` {
		t.Fatalf("Arguments retained caller mutation: %q", got)
	}
	output, err := binding.Call(t.Context(), invocation)
	if err != nil || mustText(t, output) != `{"query":"scope"}` {
		t.Fatalf("Call = %#v, %v", output, err)
	}
	if executable.calls.Load() != 1 {
		t.Fatalf("calls = %d, want 1", executable.calls.Load())
	}
}

func TestContractNormalizesBlankArgumentsToEmptyObject(t *testing.T) {
	executableSchema := json.RawMessage(`{"type":"object","additionalProperties":false}`)
	wrapped := definitionTool{definition: chat.ToolDefinition{Name: "empty", InputSchema: executableSchema}}
	binding, err := tool.Bind(wrapped)
	if err != nil {
		t.Fatal(err)
	}
	invocation, err := binding.Contract().Prepare(chat.ToolCall{ID: "call", Name: "empty"})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(invocation.Arguments()); got != "{}" {
		t.Fatalf("Arguments = %q, want {}", got)
	}
}

type definitionTool struct{ definition chat.ToolDefinition }

func (d definitionTool) Definition() chat.ToolDefinition { return d.definition }
func (definitionTool) Call(context.Context, tool.Invocation) (chat.ToolOutput, error) {
	return chat.ToolOutput{}, nil
}

func TestBindingRejectsForeignInvocation(t *testing.T) {
	first, err := tool.Bind(definitionTool{definition: chat.ToolDefinition{
		Name: "same", InputSchema: json.RawMessage(`{"type":"object"}`),
	}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := tool.Bind(definitionTool{definition: chat.ToolDefinition{
		Name: "same", InputSchema: json.RawMessage(`{"type":"object"}`),
	}})
	if err != nil {
		t.Fatal(err)
	}
	invocation, err := first.Contract().Prepare(chat.ToolCall{ID: "call", Name: "same", Arguments: `{}`})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := second.Call(t.Context(), invocation); !errors.Is(err, tool.ErrInvalidInvocation) {
		t.Fatalf("Call error = %v, want ErrInvalidInvocation", err)
	}
}

func TestZeroContractCannotPrepareInvocation(t *testing.T) {
	var contract tool.Contract
	if _, err := contract.Prepare(chat.ToolCall{ID: "call", Name: "search", Arguments: `{}`}); !errors.Is(err, tool.ErrInvalidInvocation) {
		t.Fatalf("Prepare error = %v, want ErrInvalidInvocation", err)
	}
}
