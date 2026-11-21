package tool_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
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
		binding, err = tool.Bind(tool.Wrap(executable, middleware))
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
	binding, err := tool.Bind(tool.Wrap(executable, func(next tool.Caller) tool.Caller {
		retained = next
		return tool.CallerFunc(func(context.Context, tool.Invocation) (tool.Result, error) { return tool.Result{}, nil })
	}))
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
	binding, err := tool.Bind(tool.Wrap(newStubTool("search"), func(tool.Caller) tool.Caller {
		return tool.CallerFunc(func(context.Context, tool.Invocation) (tool.Result, error) {
			return tool.Result{InputRequest: &request}, nil
		})
	}))
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
		binding, err := tool.Bind(tool.Wrap(confirmingTool{}, func(next tool.Caller) tool.Caller {
			return tool.CallerFunc(func(ctx context.Context, admitted tool.Invocation) (tool.Result, error) {
				result, err := next.Call(ctx, admitted)
				if drop {
					result = tool.Result{Output: tool.NewTextOutput("finished")}
				}
				return result, err
			})
		}))
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
	binding, err := tool.Bind(tool.Wrap(executable, func(next tool.Caller) tool.Caller {
		return tool.CallerFunc(func(ctx context.Context, admitted tool.Invocation) (tool.Result, error) {
			go func() {
				defer close(done)
				_, _ = next.Call(ctx, admitted)
			}()
			<-executable.started
			return tool.Result{}, nil
		})
	}))
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

func TestWrapRejectsAMiddlewareThatReturnsNoCaller(t *testing.T) {
	executable := &countingTool{name: "search"}
	var retained tool.Caller
	binding, err := tool.Bind(tool.Wrap(executable, func(next tool.Caller) tool.Caller { retained = next; return nil }))
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
	binding, err := tool.Bind(tool.Wrap(executable, func(next tool.Caller) tool.Caller {
		return tool.CallerFunc(func(ctx context.Context, admitted tool.Invocation) (tool.Result, error) {
			_, _ = next.Call(ctx, admitted)
			_, _ = next.Call(ctx, admitted)
			return tool.Result{}, failure
		})
	}))
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
	binding, err := tool.Bind(tool.Wrap(executable, func(next tool.Caller) tool.Caller {
		retained = next
		panic("factory failed")
	}))
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

func TestWrapComposesOutermostFirstAndSkipsNil(t *testing.T) {
	var order []string
	record := func(name string) tool.CallMiddleware {
		return func(next tool.Caller) tool.Caller {
			return tool.CallerFunc(func(ctx context.Context, invocation tool.Invocation) (tool.Result, error) {
				order = append(order, name)
				return next.Call(ctx, invocation)
			})
		}
	}
	executable := &countingTool{name: "search"}
	binding, err := tool.Bind(tool.Wrap(executable, nil, record("outer"), nil, record("inner")))
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
	if strings.Join(order, ",") != "outer,inner" || executable.calls.Load() != 1 {
		t.Fatalf("order=%v calls=%d", order, executable.calls.Load())
	}
}

func TestWrapWithoutMiddlewareReturnsTheTool(t *testing.T) {
	executable := &countingTool{name: "search"}
	if wrapped := tool.Wrap(executable); wrapped != tool.Tool(executable) {
		t.Fatalf("Wrap() = %T, want the Tool itself", wrapped)
	}
	if wrapped := tool.Wrap(executable, nil, nil); wrapped != tool.Tool(executable) {
		t.Fatalf("Wrap(nil, nil) = %T, want the Tool itself", wrapped)
	}
	if wrapped := tool.Wrap(nil, func(next tool.Caller) tool.Caller { return next }); wrapped != nil {
		t.Fatalf("Wrap hid a nil Tool behind %T", wrapped)
	}
}

func TestWrapUnwrapsToTheDecoratedTool(t *testing.T) {
	executable := &countingTool{name: "search"}
	wrapped := tool.Wrap(executable, func(next tool.Caller) tool.Caller { return next })
	unwrapper, ok := wrapped.(interface{ Unwrap() tool.Tool })
	if !ok || unwrapper.Unwrap() != tool.Tool(executable) {
		t.Fatalf("Wrap result does not unwrap to the decorated Tool: %T", wrapped)
	}
	if definition := wrapped.Definition(); definition.Name != "search" {
		t.Fatalf("Definition = %+v", definition)
	}
}

// permissiveTool overrides the validator of the Tool it decorates.
type permissiveTool struct{ tool.Tool }

func (p permissiveTool) Unwrap() tool.Tool { return p.Tool }

func (permissiveTool) InputValidator() func([]byte) error { return nil }

func TestAsSearchesTheUnwrapChainFromTheOutside(t *testing.T) {
	inner := validatingTool{declare: func() func([]byte) error { return nil }}
	forward := func(next tool.Caller) tool.Caller { return next }
	for _, test := range []struct {
		name string
		tool tool.Tool
		want any // the declaring Tool, compared by type
	}{
		{"undecorated", inner, inner},
		{"Wrap", tool.Wrap(inner, forward), inner},
		{"host decorators", decoratedTool{Tool: decoratedTool{Tool: inner}}, inner},
		{"overriding decorator", tool.Wrap(permissiveTool{Tool: inner}, forward), permissiveTool{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			declared, ok := tool.As[tool.InputValidatingTool](test.tool)
			if !ok || fmt.Sprintf("%T", declared) != fmt.Sprintf("%T", test.want) {
				t.Fatalf("As = %T, %v; want %T", declared, ok, test.want)
			}
		})
	}
}

func TestAsReportsAbsentDeclarations(t *testing.T) {
	for _, candidate := range []tool.Tool{nil, &countingTool{name: "search"}, decoratedTool{Tool: &countingTool{name: "search"}}, decoratedTool{}} {
		declared, ok := tool.As[tool.InputValidatingTool](candidate)
		if ok || declared != nil {
			t.Fatalf("As(%T) = %v, %v; want nil, false", candidate, declared, ok)
		}
	}
}
