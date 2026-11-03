package tool_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Tangerg/scope/core/tool"
)

func TestGuardLeavesContractCompilationToBinding(t *testing.T) {
	executable := newStubTool("dynamic")
	executable.definition.InputSchema = json.RawMessage(`{"type":"object","properties":{"query":{"type":"invalid"}}}`)
	guard, err := tool.NewGuard(tool.GuardConfig{Authorizer: tool.AuthorizerFunc(func(context.Context, tool.Authorization) (bool, error) {
		t.Fatal("invalid contract reached authorization")
		return true, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tool.Bind(executable, guard.Call); !errors.Is(err, tool.ErrInvalidTool) {
		t.Fatalf("invalid schema admitted: %v", err)
	}
}

func TestGuardAuthorizesValidatedInvocationBeforeExecution(t *testing.T) {
	executable := &countingTool{name: "search"}
	var inspected tool.Authorization
	guard, err := tool.NewGuard(tool.GuardConfig{
		Authorizer: tool.AuthorizerFunc(func(_ context.Context, authorization tool.Authorization) (bool, error) {
			inspected = authorization
			return true, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	binding, err := tool.Bind(executable, guard.Call)
	if err != nil {
		t.Fatal(err)
	}
	if executable.definition.Load() != 1 {
		t.Fatalf("Definition called %d times, want exactly once", executable.definition.Load())
	}
	invocation, err := binding.Contract().Prepare(tool.Request{
		Name: "search", Arguments: `{"query":"scope"}`,
	}, tool.Admission{})
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
	if binding.Contract().Definition().InputSchema[0] != '{' || string(inspected.Arguments()) != `{"query":"scope"}` {
		t.Fatal("Authorization exposed guard-owned state")
	}
	if executable.calls.Load() != 1 {
		t.Fatalf("tool calls = %d, want 1", executable.calls.Load())
	}
}

func TestGuardDenialSkipsExecution(t *testing.T) {
	executable := &countingTool{name: "search"}
	guard, err := tool.NewGuard(tool.GuardConfig{
		Authorizer: tool.AuthorizerFunc(func(context.Context, tool.Authorization) (bool, error) {
			return false, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	binding, err := tool.Bind(executable, guard.Call)
	if err != nil {
		t.Fatal(err)
	}
	invocation, err := binding.Contract().Prepare(tool.Request{
		Name: "search", Arguments: `{"query":"scope"}`,
	}, tool.Admission{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = binding.Call(t.Context(), invocation)
	failure, found := errors.AsType[*tool.Failure](err)
	if !found || failure.Kind() != tool.FailureKindRejected || failure.Cause() != nil {
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
		Authorizer: tool.AuthorizerFunc(func(context.Context, tool.Authorization) (bool, error) {
			authorizations++
			return true, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	binding, err := tool.Bind(executable, guard.Call)
	if err != nil {
		t.Fatal(err)
	}
	invocation, err := binding.Contract().Prepare(tool.Request{
		Name: "search", Arguments: `{"query":"scope"}`,
	}, tool.Admission{})
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

func TestGuardKeepsPolicyDeadlineBehindAuthorizationBoundary(t *testing.T) {
	executable := &countingTool{name: "search"}
	guard, err := tool.NewGuard(tool.GuardConfig{
		Authorizer: tool.AuthorizerFunc(func(context.Context, tool.Authorization) (bool, error) {
			return false, context.DeadlineExceeded
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	binding, err := tool.Bind(executable, guard.Call)
	if err != nil {
		t.Fatal(err)
	}
	invocation, err := binding.Contract().Prepare(tool.Request{
		Name: "search", Arguments: `{"query":"scope"}`,
	}, tool.Admission{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = binding.Call(t.Context(), invocation)
	failure, found := errors.AsType[*tool.AuthorizationError](err)
	if !found || !errors.Is(failure.Cause(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Call error = %v", err)
	}
	if executable.calls.Load() != 0 {
		t.Fatalf("canceled authorization executed tool %d times", executable.calls.Load())
	}
}

func TestGuardValidatesConstruction(t *testing.T) {
	var nilAuthorizer tool.AuthorizerFunc
	for _, config := range []tool.GuardConfig{{}, {Authorizer: nilAuthorizer}} {
		if _, err := tool.NewGuard(config); !errors.Is(err, tool.ErrInvalidTool) {
			t.Fatalf("invalid guard accepted: %v", err)
		}
	}
	var zero tool.Guard
	if _, err := zero.Call(tool.CallerFunc(func(context.Context, tool.Invocation) (tool.Result, error) {
		t.Fatal("invalid guard executed")
		return tool.Result{}, nil
	})).Call(t.Context(), tool.Invocation{}); !errors.Is(err, tool.ErrInvalidTool) {
		t.Fatalf("zero guard: %v", err)
	}
}
