package tool_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Tangerg/scope/core/tool"
)

type failingTool struct {
	effect tool.Effect
	err    error
	cancel context.CancelFunc
}

func (f failingTool) Definition() tool.Definition {
	return tool.Definition{Name: "lookup", InputSchema: json.RawMessage(`{"type":"object"}`), Effect: f.effect}
}

func (f failingTool) Call(context.Context, tool.Invocation) (tool.Result, error) {
	if f.cancel != nil {
		f.cancel()
	}
	return tool.Result{}, f.err
}

func TestBindingSettlesSafeErrorsAsDefiniteFailures(t *testing.T) {
	cause := errors.New("backend unavailable")
	undecided := &tool.AuthorizationError{}
	for _, test := range []struct {
		name     string
		effect   tool.Effect
		err      error
		cancel   bool
		definite bool
	}{
		{name: "safe error", effect: tool.EffectSafe, err: cause, definite: true},
		{name: "safe canceled execution", effect: tool.EffectSafe, err: cause, cancel: true},
		{name: "safe undecided authorization", effect: tool.EffectSafe, err: undecided},
		{name: "idempotent error", effect: tool.EffectIdempotent, err: cause},
		{name: "unsafe error", effect: tool.EffectUnsafe, err: cause},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			executable := failingTool{effect: test.effect, err: test.err}
			if test.cancel {
				executable.cancel = cancel
			}
			binding, err := tool.Bind(executable)
			if err != nil {
				t.Fatal(err)
			}
			invocation, err := binding.Contract().Prepare(tool.Request{Name: "lookup"}, tool.Admission{})
			if err != nil {
				t.Fatal(err)
			}
			_, callErr := binding.Call(ctx, invocation)
			failure, definite := tool.ClassifyFailure(callErr)
			if definite != test.definite {
				t.Fatalf("definite = %t, error = %v", definite, callErr)
			}
			if !definite {
				if !errors.Is(callErr, test.err) {
					t.Fatalf("unsettled error lost its cause: %v", callErr)
				}
				return
			}
			if text, _ := failure.Output().Text(); failure.Kind() != tool.FailureKindFailed || text != `tool "lookup" failed` || !errors.Is(failure.Cause(), cause) {
				t.Fatalf("failure = %v %q", failure.Kind(), text)
			}
		})
	}
}

func TestBindingSettlesInvalidResultsAtTheSameExecutionBoundary(t *testing.T) {
	type response struct {
		Count int `json:"count"`
	}
	for _, test := range []struct {
		name     string
		effect   tool.Effect
		details  string
		cancel   bool
		definite bool
		want     error
	}{
		{name: "safe schema mismatch", effect: tool.EffectSafe, details: `{"count":"wrong"}`, definite: true, want: tool.ErrInvalidResult},
		{name: "safe invalid result", effect: tool.EffectSafe, details: `{`, definite: true, want: tool.ErrInvalidOutput},
		{name: "safe canceled execution", effect: tool.EffectSafe, details: `{"count":"wrong"}`, cancel: true, want: tool.ErrInvalidResult},
		{name: "idempotent schema mismatch", effect: tool.EffectIdempotent, details: `{"count":"wrong"}`, want: tool.ErrInvalidResult},
		{name: "unsafe schema mismatch", effect: tool.EffectUnsafe, details: `{"count":"wrong"}`, want: tool.ErrInvalidResult},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			executable, err := tool.NewFunc(tool.FuncConfig{Name: "count", Effect: test.effect}, func(context.Context, struct{}) (response, error) {
				return response{Count: 2}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			binding, err := tool.Bind(executable, func(next tool.Caller) tool.Caller {
				return tool.CallerFunc(func(ctx context.Context, invocation tool.Invocation) (tool.Result, error) {
					result, callErr := next.Call(ctx, invocation)
					result.Output.Details = []byte(test.details)
					if test.cancel {
						cancel()
					}
					return result, callErr
				})
			})
			if err != nil {
				t.Fatal(err)
			}
			invocation, err := binding.Contract().Prepare(tool.Request{Name: "count"}, tool.Admission{})
			if err != nil {
				t.Fatal(err)
			}
			result, callErr := binding.Call(ctx, invocation)
			if result.InputRequest != nil || len(result.Output.Content) != 0 || len(result.Output.Details) != 0 {
				t.Fatalf("invalid result escaped: %+v", result)
			}
			failure, definite := tool.ClassifyFailure(callErr)
			if definite != test.definite {
				t.Fatalf("definite=%t, error=%v", definite, callErr)
			}
			cause := callErr
			if definite {
				text, _ := failure.Output().Text()
				if failure.Kind() != tool.FailureKindFailed || text != `tool "count" failed` {
					t.Fatalf("failure=%s output=%q", failure.Kind(), text)
				}
				cause = failure.Cause()
			}
			if !errors.Is(cause, test.want) {
				t.Fatalf("lost result validation diagnostic: %v", cause)
			}
		})
	}
}
