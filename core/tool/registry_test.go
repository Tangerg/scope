package tool_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Tangerg/scope/core/tool"
)

type stubTool struct {
	definition tool.Definition
	result     tool.Output
}

func (s *stubTool) Definition() tool.Definition { return s.definition }

func (s *stubTool) Call(context.Context, tool.Invocation) (tool.Result, error) {
	return tool.Result{Output: s.result.Clone()}, nil
}

func newStubTool(name string) *stubTool {
	return &stubTool{
		definition: tool.Definition{
			Name:        name,
			Description: "test " + name,
			InputSchema: json.RawMessage(`{"type":"object"}`),
		},
		result: tool.NewTextOutput(name + " result"),
	}
}

func TestRegistryResolveAndDefinitions(t *testing.T) {
	registry, err := tool.NewRegistry(boundStubTool(t, "zeta"), boundStubTool(t, "alpha"))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}

	value, ok := registry.Resolve("alpha")
	if !ok {
		t.Fatal("Resolve(alpha) did not find registered tool")
	}
	invocation, err := value.Contract().Prepare(tool.Request{Name: "alpha", Arguments: `{}`}, tool.Admission{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := value.Call(t.Context(), invocation)
	if err != nil || mustText(t, result.Output) != "alpha result" {
		t.Fatalf("Call = %#v, %v", result, err)
	}
	if _, ok := registry.Resolve("missing"); ok {
		t.Fatal("Resolve(missing) unexpectedly succeeded")
	}

	definitions := registry.Definitions()
	if len(definitions) != 2 || definitions[0].Name != "alpha" || definitions[1].Name != "zeta" {
		t.Fatalf("Definitions = %#v, want alpha then zeta", definitions)
	}
	definitions[0].InputSchema[0] = '['
	if got := string(registry.Definitions()[0].InputSchema); got != `{"type":"object"}` {
		t.Fatalf("mutating returned definition changed registry schema to %q", got)
	}
}

func TestRegistryRejectsDuplicateAndUnboundTools(t *testing.T) {
	if _, err := tool.NewRegistry(boundStubTool(t, "same"), boundStubTool(t, "other"), boundStubTool(t, "same")); err == nil || err.Error() != `tool: duplicate tool: "same" appears more than once` {
		t.Fatalf("duplicate error = %v", err)
	}
	if _, err := tool.NewRegistry(boundStubTool(t, "valid"), tool.Binding{}); !errors.Is(err, tool.ErrInvalidTool) {
		t.Fatalf("zero binding admitted: %v", err)
	}
}

func TestRegistryZeroValueIsEmpty(t *testing.T) {
	var registry tool.Registry
	if value, ok := registry.Resolve("tool"); ok || value.Contract().Name() != "" {
		t.Fatal("zero Registry resolved a tool")
	}
	if definitions := registry.Definitions(); len(definitions) != 0 {
		t.Fatalf("zero Registry definitions = %v", definitions)
	}
}

func boundStubTool(t *testing.T, name string) tool.Binding {
	t.Helper()
	binding, err := tool.Bind(newStubTool(name))
	if err != nil {
		t.Fatal(err)
	}
	return binding
}
