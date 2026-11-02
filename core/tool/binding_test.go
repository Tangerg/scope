package tool_test

import (
	"context"
	"encoding/json"
	"encoding/json/jsontext"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/Tangerg/scope/core/tool"
)

type countingTool struct {
	name       string
	definition atomic.Int64
	calls      atomic.Int64
}

func (c *countingTool) Definition() tool.Definition {
	c.definition.Add(1)
	return tool.Definition{
		Name: c.name,
		InputSchema: json.RawMessage(
			`{"type":"object","properties":{"query":{"type":"string","minLength":2}},"required":["query"],"additionalProperties":false}`,
		),
	}
}

func (c *countingTool) Call(_ context.Context, invocation tool.Invocation) (tool.Result, error) {
	c.calls.Add(1)
	return tool.Result{Output: tool.NewTextOutput(string(invocation.Arguments()))}, nil
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
	if definition := contract.Definition(); definition.Name != "search" || !jsontext.Value(definition.InputSchema).IsValid() {
		t.Fatalf("Contract retained a caller's definition mutation: %+v", definition)
	}

	for _, call := range []tool.Request{
		{Name: "", Arguments: `{"query":"ok"}`},
		{Name: "other", Arguments: `{"query":"ok"}`},
		{Name: "search", Arguments: `{`},
		{Name: "search", Arguments: `{"query":"x"}`},
		{Name: "search", Arguments: `{"query":"ok","extra":true}`},
	} {
		if _, prepareErr := contract.Prepare(call, tool.Admission{}); !errors.Is(prepareErr, tool.ErrInvalidInvocation) {
			t.Errorf("Prepare(%+v) error = %v, want ErrInvalidInvocation", call, prepareErr)
		}
	}
	if executable.calls.Load() != 0 {
		t.Fatalf("invalid promotion executed Tool %d times", executable.calls.Load())
	}

	invocation, err := contract.Prepare(tool.Request{Name: "search", Arguments: `{"query":"scope"}`}, tool.Admission{})
	if err != nil {
		t.Fatal(err)
	}
	arguments := invocation.Arguments()
	arguments[0] = '['
	if got := string(invocation.Arguments()); got != `{"query":"scope"}` {
		t.Fatalf("Arguments retained caller mutation: %q", got)
	}
	output, err := binding.Call(t.Context(), invocation)
	if err != nil || mustText(t, output.Output) != `{"query":"scope"}` {
		t.Fatalf("Call = %#v, %v", output, err)
	}
	if executable.calls.Load() != 1 {
		t.Fatalf("calls = %d, want 1", executable.calls.Load())
	}
}

func TestContractNormalizesBlankArgumentsToEmptyObject(t *testing.T) {
	executableSchema := json.RawMessage(`{"type":"object","additionalProperties":false}`)
	wrapped := definitionTool{definition: tool.Definition{Name: "empty", InputSchema: executableSchema}}
	binding, err := tool.Bind(wrapped)
	if err != nil {
		t.Fatal(err)
	}
	invocation, err := binding.Contract().Prepare(tool.Request{Name: "empty"}, tool.Admission{})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(invocation.Arguments()); got != "{}" {
		t.Fatalf("Arguments = %q, want {}", got)
	}
}

type definitionTool struct{ definition tool.Definition }

func (d definitionTool) Definition() tool.Definition { return d.definition }
func (definitionTool) Call(context.Context, tool.Invocation) (tool.Result, error) {
	return tool.Result{}, nil
}

func TestBindingRejectsForeignInvocation(t *testing.T) {
	first, err := tool.Bind(definitionTool{definition: tool.Definition{
		Name: "same", InputSchema: json.RawMessage(`{"type":"object"}`),
	}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := tool.Bind(definitionTool{definition: tool.Definition{
		Name: "same", InputSchema: json.RawMessage(`{"type":"object"}`),
	}})
	if err != nil {
		t.Fatal(err)
	}
	invocation, err := first.Contract().Prepare(tool.Request{Name: "same", Arguments: `{}`}, tool.Admission{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := second.Call(t.Context(), invocation); !errors.Is(err, tool.ErrInvalidInvocation) {
		t.Fatalf("Call error = %v, want ErrInvalidInvocation", err)
	}
}

func TestZeroContractCannotPrepareInvocation(t *testing.T) {
	var contract tool.Contract
	if _, err := contract.Prepare(tool.Request{Name: "search", Arguments: `{}`}, tool.Admission{}); !errors.Is(err, tool.ErrInvalidInvocation) {
		t.Fatalf("Prepare error = %v, want ErrInvalidInvocation", err)
	}
}
