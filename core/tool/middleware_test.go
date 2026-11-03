package tool_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Tangerg/scope/core/jsonschema"
	"github.com/Tangerg/scope/core/tool"
)

func TestMiddlewareCannotReplaceInvocationOrRepeatExecution(t *testing.T) {
	for _, replace := range []bool{false, true} {
		executable := &countingTool{name: "search"}
		var binding tool.Binding
		middleware := func(next tool.Caller) tool.Caller {
			return tool.CallerFunc(func(ctx context.Context, admitted tool.Invocation) (tool.Result, error) {
				if replace {
					changed, err := binding.Contract().Prepare(tool.Request{Name: "search", Arguments: `{"query":"other"}`}, tool.Admission{IdempotencyKey: "different"})
					if err != nil {
						t.Fatal(err)
					}
					_, _ = next.Call(ctx, changed)
				} else {
					if _, err := next.Call(ctx, admitted); err != nil {
						t.Fatal(err)
					}
					_, _ = next.Call(ctx, admitted)
				}
				return tool.Result{}, nil
			})
		}
		var err error
		binding, err = tool.Bind(executable, middleware)
		if err != nil {
			t.Fatal(err)
		}
		admitted, err := binding.Contract().Prepare(tool.Request{Name: "search", Arguments: `{"query":"scope"}`}, tool.Admission{IdempotencyKey: "original"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := binding.Call(t.Context(), admitted); !errors.Is(err, tool.ErrInvalidInvocation) {
			t.Fatalf("middleware violation was hidden: %v", err)
		}
		wantCalls := int64(1)
		if replace {
			wantCalls = 0
		}
		if executable.calls.Load() != wantCalls {
			t.Fatalf("executions=%d, want %d", executable.calls.Load(), wantCalls)
		}
	}
}

func TestMiddlewareCannotRetainExecutionAuthorityAfterReturn(t *testing.T) {
	executable := &countingTool{name: "search"}
	var retained tool.Caller
	binding, err := tool.Bind(executable, func(next tool.Caller) tool.Caller {
		retained = next
		return tool.CallerFunc(func(context.Context, tool.Invocation) (tool.Result, error) { return tool.Result{}, nil })
	})
	if err != nil {
		t.Fatal(err)
	}
	admitted, err := binding.Contract().Prepare(tool.Request{Name: "search", Arguments: `{"query":"scope"}`}, tool.Admission{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := binding.Call(t.Context(), admitted); err != nil {
		t.Fatal(err)
	}
	if _, err := retained.Call(t.Context(), admitted); !errors.Is(err, tool.ErrInvalidInvocation) || executable.calls.Load() != 0 {
		t.Fatalf("retained caller executed: %v", err)
	}
}

func TestMiddlewareCannotOriginateInputRequest(t *testing.T) {
	schema, err := jsonschema.For[bool]()
	if err != nil {
		t.Fatal(err)
	}
	request, err := tool.NewInputRequest(tool.InputRequestConfig{Prompt: []byte(`"approve"`), ResponseSchema: schema, ContinuationState: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	binding, err := tool.Bind(newStubTool("search"), func(tool.Caller) tool.Caller {
		return tool.CallerFunc(func(context.Context, tool.Invocation) (tool.Result, error) {
			return tool.Result{InputRequest: &request}, nil
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	admitted, err := binding.Contract().Prepare(tool.Request{Name: "search"}, tool.Admission{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := binding.Call(t.Context(), admitted); !errors.Is(err, tool.ErrInvalidResult) {
		t.Fatalf("middleware manufactured a wait: %v", err)
	}
}

func TestMiddlewarePreservesTheToolsContinuation(t *testing.T) {
	for _, drop := range []bool{false, true} {
		binding, err := tool.Bind(confirmingTool{}, func(next tool.Caller) tool.Caller {
			return tool.CallerFunc(func(ctx context.Context, admitted tool.Invocation) (tool.Result, error) {
				result, err := next.Call(ctx, admitted)
				if drop {
					result = tool.Result{Output: tool.NewTextOutput("finished")}
				}
				return result, err
			})
		})
		if err != nil {
			t.Fatal(err)
		}
		admitted, err := binding.Contract().Prepare(tool.Request{Name: "confirm"}, tool.Admission{})
		if err != nil {
			t.Fatal(err)
		}
		result, err := binding.Call(t.Context(), admitted)
		if drop {
			if !errors.Is(err, tool.ErrInvalidResult) {
				t.Fatalf("middleware completed a suspended tool: %v", err)
			}
			continue
		}
		if err != nil || result.InputRequest == nil {
			t.Fatalf("transparent middleware lost continuation: %v", err)
		}
		continuation, err := result.InputRequest.Continuation([]byte(`9007199254740993`))
		if err != nil {
			t.Fatal(err)
		}
		resumed, err := binding.Contract().Prepare(tool.Request{Name: "confirm"}, tool.Admission{Continuation: &continuation})
		if err != nil {
			t.Fatal(err)
		}
		completed, err := binding.Call(t.Context(), resumed)
		if err != nil || completed.InputRequest != nil {
			t.Fatalf("tool could not complete: %v", err)
		}
	}
}

type blockedTool struct {
	started chan struct{}
	release chan struct{}
}

func (b blockedTool) Definition() tool.Definition {
	return newStubTool("blocked").Definition()
}

func (b blockedTool) Call(context.Context, tool.Invocation) (tool.Result, error) {
	close(b.started)
	<-b.release
	return tool.Result{Output: tool.NewTextOutput("completed")}, nil
}

func TestMiddlewareCannotReturnSuccessBeforeExecutionFinishes(t *testing.T) {
	executable := blockedTool{started: make(chan struct{}), release: make(chan struct{})}
	done := make(chan struct{})
	binding, err := tool.Bind(executable, func(next tool.Caller) tool.Caller {
		return tool.CallerFunc(func(ctx context.Context, admitted tool.Invocation) (tool.Result, error) {
			go func() {
				defer close(done)
				_, _ = next.Call(ctx, admitted)
			}()
			<-executable.started
			return tool.Result{}, nil
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	admitted, err := binding.Contract().Prepare(tool.Request{Name: "blocked"}, tool.Admission{})
	if err != nil {
		t.Fatal(err)
	}
	_, callErr := binding.Call(t.Context(), admitted)
	close(executable.release)
	<-done
	if !errors.Is(callErr, tool.ErrInvalidResult) {
		t.Fatalf("unfinished call appeared complete: %v", callErr)
	}
	if _, definite := tool.ClassifyFailure(callErr); definite {
		t.Fatal("unfinished execution was labeled a definite failure")
	}
}

func TestBindingRejectsAMiddlewareThatReturnsNoCaller(t *testing.T) {
	executable := &countingTool{name: "search"}
	var retained tool.Caller
	binding, err := tool.Bind(executable, func(next tool.Caller) tool.Caller { retained = next; return nil })
	if err != nil {
		t.Fatal(err)
	}
	admitted, err := binding.Contract().Prepare(tool.Request{Name: "search", Arguments: `{"query":"scope"}`}, tool.Admission{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := binding.Call(t.Context(), admitted); !errors.Is(err, tool.ErrInvalidTool) || executable.calls.Load() != 0 {
		t.Fatalf("nil middleware caller reached execution: %v", err)
	}
	if _, err := retained.Call(t.Context(), admitted); !errors.Is(err, tool.ErrInvalidInvocation) || executable.calls.Load() != 0 {
		t.Fatalf("failed factory retained execution authority: %v", err)
	}
}

func TestMiddlewareViolationCannotBecomeADefiniteRejection(t *testing.T) {
	executable := &countingTool{name: "search"}
	failure, err := tool.NewFailure(tool.FailureConfig{Kind: tool.FailureKindRejected, Output: tool.NewTextOutput("rejected")})
	if err != nil {
		t.Fatal(err)
	}
	binding, err := tool.Bind(executable, func(next tool.Caller) tool.Caller {
		return tool.CallerFunc(func(ctx context.Context, admitted tool.Invocation) (tool.Result, error) {
			_, _ = next.Call(ctx, admitted)
			_, _ = next.Call(ctx, admitted)
			return tool.Result{}, failure
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	admitted, err := binding.Contract().Prepare(tool.Request{Name: "search", Arguments: `{"query":"scope"}`}, tool.Admission{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = binding.Call(t.Context(), admitted)
	if !errors.Is(err, tool.ErrInvalidInvocation) || executable.calls.Load() != 1 {
		t.Fatalf("violation hidden behind failure: %v", err)
	}
	if _, definite := tool.ClassifyFailure(err); definite {
		t.Fatal("middleware violation was classified as rejection before execution")
	}
}

func TestPanickingMiddlewareFactoryCannotRetainExecutionAuthority(t *testing.T) {
	executable := &countingTool{name: "search"}
	var retained tool.Caller
	binding, err := tool.Bind(executable, func(next tool.Caller) tool.Caller {
		retained = next
		panic("factory failed")
	})
	if err != nil {
		t.Fatal(err)
	}
	admitted, err := binding.Contract().Prepare(tool.Request{Name: "search", Arguments: `{"query":"scope"}`}, tool.Admission{})
	if err != nil {
		t.Fatal(err)
	}
	func() {
		defer func() {
			if recovered := recover(); recovered != "factory failed" {
				t.Fatalf("factory panic=%v", recovered)
			}
		}()
		_, _ = binding.Call(t.Context(), admitted)
	}()
	if _, err := retained.Call(t.Context(), admitted); !errors.Is(err, tool.ErrInvalidInvocation) || executable.calls.Load() != 0 {
		t.Fatalf("panicking factory retained execution authority: %v", err)
	}
}
