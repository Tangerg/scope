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
			if text, _ := failure.Output().Text(); failure.Kind() != tool.FailureKindFailed || text != `error: tool "lookup" failed` || !errors.Is(failure.Cause(), cause) {
				t.Fatalf("failure = %v %q", failure.Kind(), text)
			}
		})
	}
}
