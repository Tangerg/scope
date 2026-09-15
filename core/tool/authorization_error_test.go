package tool_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

func TestGuardSeparatesInternalErrorsFromCurrentInvocation(t *testing.T) {
	inner, err := tool.NewFailure(tool.FailureConfig{
		Kind: tool.FailureKindRejected, Cause: errors.New("private policy detail"),
		Output: chat.NewTextToolOutput("private dependency output"),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, cause := range []error{
		errors.New("private policy detail"), inner, fmt.Errorf("dependency: %w", inner),
		context.DeadlineExceeded, errors.Join(context.DeadlineExceeded, inner),
	} {
		for _, cancelExecution := range []bool{false, true} {
			t.Run(fmt.Sprintf("%T/cancel=%v", cause, cancelExecution), func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				executable := &countingTool{name: "search"}
				guard, err := tool.NewGuard(tool.GuardConfig{
					Tool: executable,
					Authorizer: tool.AuthorizerFunc(func(context.Context, tool.Authorization) (bool, error) {
						if cancelExecution {
							cancel()
						}
						return true, cause
					}),
				})
				if err != nil {
					t.Fatal(err)
				}
				binding, err := tool.Bind(guard)
				if err != nil {
					t.Fatal(err)
				}
				invocation, err := binding.Contract().Prepare(chat.ToolCall{ID: "current", Name: "search", Arguments: `{"query":"scope"}`})
				if err != nil {
					t.Fatal(err)
				}
				_, callErr := binding.Call(ctx, invocation)
				boundary, found := errors.AsType[*tool.AuthorizationError](callErr)
				if !found || !reflect.ValueOf(boundary.Cause()).Equal(reflect.ValueOf(cause)) || executable.calls.Load() != 0 {
					t.Fatalf("error=%v calls=%d", callErr, executable.calls.Load())
				}
				if _, found := errors.AsType[*tool.Failure](callErr); found || errors.Is(callErr, cause) ||
					errors.Is(callErr, context.DeadlineExceeded) || strings.Contains(callErr.Error(), "private") {
					t.Fatalf("internal policy error crossed invocation boundary: %v", callErr)
				}
				if errors.Is(callErr, context.Canceled) != cancelExecution {
					t.Fatalf("execution cancellation = %v, error=%v", cancelExecution, callErr)
				}
			})
		}
	}
}

func TestGuardDoesNotExecuteAfterPolicyCancelsExecution(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	executable := &countingTool{name: "search"}
	guard, err := tool.NewGuard(tool.GuardConfig{
		Tool: executable,
		Authorizer: tool.AuthorizerFunc(func(context.Context, tool.Authorization) (bool, error) {
			cancel()
			return true, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	binding, err := tool.Bind(guard)
	if err != nil {
		t.Fatal(err)
	}
	invocation, err := binding.Contract().Prepare(chat.ToolCall{ID: "current", Name: "search", Arguments: `{"query":"scope"}`})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := binding.Call(ctx, invocation); !errors.Is(err, context.Canceled) || executable.calls.Load() != 0 {
		t.Fatalf("error=%v calls=%d", err, executable.calls.Load())
	}
}

func TestAuthorizationErrorHasSafeZeroBehavior(t *testing.T) {
	for _, value := range []*tool.AuthorizationError{nil, {}} {
		if value.Error() != "tool: authorization failed" || value.Cause() != nil {
			t.Fatalf("zero authorization error = %v", value)
		}
	}
}
