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

func TestRegistryRegisterIsAtomic(t *testing.T) {
	registry, err := tool.NewRegistry(boundStubTool(t, "existing"))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}

	err = registry.Register(boundStubTool(t, "new"), boundStubTool(t, "existing"))
	if !errors.Is(err, tool.ErrDuplicateTool) {
		t.Fatalf("Register duplicate error = %v", err)
	}
	if _, ok := registry.Resolve("new"); ok {
		t.Fatal("failed batch partially registered new tool")
	}

	err = registry.Register(boundStubTool(t, "same"), boundStubTool(t, "same"))
	if !errors.Is(err, tool.ErrDuplicateTool) {
		t.Fatalf("Register batch duplicate error = %v", err)
	}
}

func TestRegistryReportsFirstRegisteredConflictInBatchOrder(t *testing.T) {
	names := []string{"a", "b", "c", "d", "e", "f", "g", "h"}
	existing := make([]tool.Binding, len(names))
	for index, name := range names {
		existing[index] = boundStubTool(t, name)
	}
	registry, err := tool.NewRegistry(existing...)
	if err != nil {
		t.Fatal(err)
	}
	for range 32 {
		batch := []tool.Binding{boundStubTool(t, "fresh")}
		for index := len(names) - 1; index >= 0; index-- {
			batch = append(batch, boundStubTool(t, names[index]))
		}
		err := registry.Register(batch...)
		if want := `tool: duplicate tool: "h" is already registered`; err == nil || err.Error() != want {
			t.Fatalf("Register error = %v, want %s", err, want)
		}
	}
}

func TestRegistryRejectsUnboundTools(t *testing.T) {
	var registry tool.Registry
	if err := registry.Register(boundStubTool(t, "valid"), tool.Binding{}); !errors.Is(err, tool.ErrInvalidTool) {
		t.Fatalf("zero binding admitted: %v", err)
	}
	if _, ok := registry.Resolve("valid"); ok {
		t.Fatal("invalid batch partially registered")
	}
	var nilRegistry *tool.Registry
	if err := nilRegistry.Register(boundStubTool(t, "tool")); !errors.Is(err, tool.ErrInvalidRegistry) {
		t.Fatalf("nil registry: %v", err)
	}
	if value, ok := nilRegistry.Resolve("tool"); ok || value.Contract().Name() != "" {
		t.Fatal("nil registry resolved a tool")
	}
}

func TestRegistryZeroValue(t *testing.T) {
	var registry tool.Registry
	if err := registry.Register(boundStubTool(t, "zero")); err != nil {
		t.Fatalf("zero Registry.Register: %v", err)
	}
	if _, ok := registry.Resolve("zero"); !ok {
		t.Fatal("zero Registry did not resolve registered tool")
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
