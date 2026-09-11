package tool_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

type stubTool struct {
	definition chat.ToolDefinition
	result     chat.ToolOutput
}

func (s *stubTool) Definition() chat.ToolDefinition { return s.definition }
func (s *stubTool) Call(context.Context, tool.Invocation) (chat.ToolOutput, error) {
	return s.result.Clone(), nil
}

func newStubTool(name string) *stubTool {
	return &stubTool{
		definition: chat.ToolDefinition{
			Name:        name,
			Description: "test " + name,
			InputSchema: json.RawMessage(`{"type":"object"}`),
		},
		result: chat.NewTextToolOutput(name + " result"),
	}
}

func TestRegistryResolveAndDefinitions(t *testing.T) {
	registry, err := tool.NewRegistry(newStubTool("zeta"), newStubTool("alpha"))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}

	value, ok := registry.Resolve("alpha")
	if !ok {
		t.Fatal("Resolve(alpha) did not find registered tool")
	}
	invocation, err := value.Contract().Prepare(chat.ToolCall{ID: "call", Name: "alpha", Arguments: `{}`})
	if err != nil {
		t.Fatal(err)
	}
	result, err := value.Call(t.Context(), invocation)
	if err != nil || mustText(t, result) != "alpha result" {
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
	registry, err := tool.NewRegistry(newStubTool("existing"))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}

	err = registry.Register(newStubTool("new"), newStubTool("existing"))
	if !errors.Is(err, tool.ErrDuplicateTool) {
		t.Fatalf("Register duplicate error = %v", err)
	}
	if _, ok := registry.Resolve("new"); ok {
		t.Fatal("failed batch partially registered new tool")
	}

	err = registry.Register(newStubTool("same"), newStubTool("same"))
	if !errors.Is(err, tool.ErrDuplicateTool) {
		t.Fatalf("Register batch duplicate error = %v", err)
	}
}

func TestRegistryRejectsInvalidTools(t *testing.T) {
	var typedNil *stubTool
	invalidDefinition := newStubTool("bad")
	invalidDefinition.definition.InputSchema = json.RawMessage(`[]`)

	for _, test := range []struct {
		name  string
		value tool.Tool
	}{
		{name: "nil"},
		{name: "typed nil", value: typedNil},
		{name: "invalid definition", value: invalidDefinition},
	} {
		t.Run(test.name, func(t *testing.T) {
			registry := &tool.Registry{}
			if err := registry.Register(test.value); !errors.Is(err, tool.ErrInvalidTool) {
				t.Fatalf("Register error = %v", err)
			}
		})
	}

	var nilRegistry *tool.Registry
	if err := nilRegistry.Register(newStubTool("tool")); !errors.Is(err, tool.ErrInvalidRegistry) {
		t.Fatalf("nil Registry.Register error = %v", err)
	}
	if value, ok := nilRegistry.Resolve("tool"); ok || value.Contract().Definition().Name != "" {
		t.Fatalf("nil Registry.Resolve = %#v, %v", value, ok)
	}
}

func TestRegistryZeroValue(t *testing.T) {
	var registry tool.Registry
	if err := registry.Register(newStubTool("zero")); err != nil {
		t.Fatalf("zero Registry.Register: %v", err)
	}
	if _, ok := registry.Resolve("zero"); !ok {
		t.Fatal("zero Registry did not resolve registered tool")
	}
}
