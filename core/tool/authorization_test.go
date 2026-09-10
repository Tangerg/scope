package tool_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

func TestGuardFreezesDefinitionAndLeavesCompilationToBinding(t *testing.T) {
	executable := newStubTool("dynamic")
	executable.definition.InputSchema = json.RawMessage(`{"type":"object","properties":{"query":{"type":"invalid"}}}`)
	guard, err := tool.NewGuard(tool.GuardConfig{
		Tool: executable,
		Authorizer: tool.AuthorizerFunc(func(context.Context, tool.Authorization) error {
			t.Fatal("invalid schema must fail before authorization")
			return nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	executable.definition.InputSchema = json.RawMessage(`{"type":"object"}`)
	if _, bindErr := tool.Bind(guard); !errors.Is(bindErr, tool.ErrInvalidTool) {
		t.Fatalf("Bind accepted frozen invalid schema: %v", bindErr)
	}
}

func TestGuardAuthorizesValidatedInvocationBeforeExecution(t *testing.T) {
	executable := &countingTool{name: "search"}
	var inspected tool.Authorization
	guard, err := tool.NewGuard(tool.GuardConfig{
		Tool: executable,
		Authorizer: tool.AuthorizerFunc(func(_ context.Context, authorization tool.Authorization) error {
			inspected = authorization
			return nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if executable.definition.Load() != 1 {
		t.Fatalf("Definition called %d times, want exactly once", executable.definition.Load())
	}
	binding, err := tool.Bind(guard)
	if err != nil {
		t.Fatal(err)
	}
	invocation, err := binding.Prepare(chat.ToolCall{
		ID: "call", Name: "search", Arguments: `{"query":"scope"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := binding.Call(t.Context(), invocation); err != nil {
		t.Fatal(err)
	}
	if inspected.Definition().Name != "search" || string(inspected.Arguments()) != `{"query":"scope"}` {
		t.Fatalf("Authorization = (%#v, %s)", inspected.Definition(), inspected.Arguments())
	}
	definition, arguments := inspected.Definition(), inspected.Arguments()
	definition.InputSchema[0] = '['
	arguments[0] = '['
	if guard.Definition().InputSchema[0] != '{' || string(inspected.Arguments()) != `{"query":"scope"}` {
		t.Fatal("Authorization exposed guard-owned state")
	}
	if executable.calls.Load() != 1 {
		t.Fatalf("tool calls = %d, want 1", executable.calls.Load())
	}
}

func TestGuardDenialPreservesCauseAndSkipsExecution(t *testing.T) {
	denied := errors.New("tenant policy")
	executable := &countingTool{name: "search"}
	guard, err := tool.NewGuard(tool.GuardConfig{
		Tool: executable,
		Authorizer: tool.AuthorizerFunc(func(context.Context, tool.Authorization) error {
			return denied
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	binding, err := tool.Bind(guard)
	if err != nil {
		t.Fatal(err)
	}
	invocation, err := binding.Prepare(chat.ToolCall{
		ID: "call", Name: "search", Arguments: `{"query":"scope"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = binding.Call(t.Context(), invocation)
	if !errors.Is(err, tool.ErrAuthorizationDenied) || !errors.Is(err, denied) {
		t.Fatalf("Call error = %v", err)
	}
	if executable.calls.Load() != 0 {
		t.Fatalf("denied tool executed %d times", executable.calls.Load())
	}
}

func TestGuardHonorsCancellationBeforePolicy(t *testing.T) {
	authorizations := 0
	executable := &countingTool{name: "search"}
	guard, err := tool.NewGuard(tool.GuardConfig{
		Tool: executable,
		Authorizer: tool.AuthorizerFunc(func(context.Context, tool.Authorization) error {
			authorizations++
			return nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	binding, err := tool.Bind(guard)
	if err != nil {
		t.Fatal(err)
	}
	invocation, err := binding.Prepare(chat.ToolCall{
		ID: "call", Name: "search", Arguments: `{"query":"scope"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := binding.Call(ctx, invocation); !errors.Is(err, context.Canceled) {
		t.Fatalf("Call error = %v, want context cancellation", err)
	}
	if authorizations != 0 || executable.calls.Load() != 0 {
		t.Fatalf("authorizations = %d, tool calls = %d", authorizations, executable.calls.Load())
	}
}

func TestGuardPreservesPolicyCancellationSemantics(t *testing.T) {
	executable := &countingTool{name: "search"}
	guard, err := tool.NewGuard(tool.GuardConfig{
		Tool: executable,
		Authorizer: tool.AuthorizerFunc(func(context.Context, tool.Authorization) error {
			return context.DeadlineExceeded
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	binding, err := tool.Bind(guard)
	if err != nil {
		t.Fatal(err)
	}
	invocation, err := binding.Prepare(chat.ToolCall{
		ID: "call", Name: "search", Arguments: `{"query":"scope"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = binding.Call(t.Context(), invocation)
	if !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, tool.ErrAuthorizationDenied) {
		t.Fatalf("Call error = %v", err)
	}
	if executable.calls.Load() != 0 {
		t.Fatalf("canceled authorization executed tool %d times", executable.calls.Load())
	}
}

func TestGuardValidatesConstructionAndPreservesCapabilities(t *testing.T) {
	var nilAuthorizer tool.AuthorizerFunc
	allow := tool.AuthorizerFunc(func(context.Context, tool.Authorization) error { return nil })
	for _, config := range []tool.GuardConfig{
		{},
		{Authorizer: allow},
		{Tool: markedTool{}},
		{Tool: markedTool{}, Authorizer: nilAuthorizer},
	} {
		if _, err := tool.NewGuard(config); !errors.Is(err, tool.ErrInvalidTool) {
			t.Fatalf("NewGuard(%#v) error = %v", config, err)
		}
	}

	guard, err := tool.NewGuard(tool.GuardConfig{
		Tool: markedTool{},
		Authorizer: tool.AuthorizerFunc(func(context.Context, tool.Authorization) error {
			return nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	marker, found, err := tool.Capability[capabilityMarker](guard)
	if err != nil || !found || marker.Marker() != "inner" {
		t.Fatalf("Capability = (%v, %v, %v)", marker, found, err)
	}

	var zero tool.Guard
	if _, err := zero.Call(t.Context(), tool.Invocation{}); !errors.Is(err, tool.ErrInvalidTool) {
		t.Fatalf("zero Guard.Call error = %v", err)
	}
}
