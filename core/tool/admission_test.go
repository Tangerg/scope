package tool_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/Tangerg/scope/core/tool"
)

func TestAdmissionOwnsIdempotencyAcrossContinuation(t *testing.T) {
	const key tool.IdempotencyKey = "host-allocated-call"
	var observed []tool.IdempotencyKey
	binding, err := tool.Bind(confirmingTool{}, func(next tool.Caller) tool.Caller {
		return tool.CallerFunc(func(ctx context.Context, invocation tool.Invocation) (tool.Result, error) {
			observed = append(observed, invocation.IdempotencyKey())
			return next.Call(ctx, invocation)
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	request := tool.Request{Name: "confirm", Arguments: `{"idempotency_key":"model-proposed"}`}
	admission := tool.Admission{IdempotencyKey: key}
	initial, err := binding.Contract().Prepare(request, admission)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := binding.Call(t.Context(), initial)
	if err != nil || pending.InputRequest == nil {
		t.Fatalf("initial call = %+v, %v", pending, err)
	}
	continuation, err := pending.InputRequest.Continuation([]byte(`9007199254740993`))
	if err != nil {
		t.Fatal(err)
	}
	admission.Continuation = &continuation
	resumed, err := binding.Contract().Prepare(request, admission)
	if err != nil {
		t.Fatal(err)
	}
	admission.IdempotencyKey = "another-call"
	completed, err := binding.Call(t.Context(), resumed)
	if err != nil || completed.InputRequest != nil {
		t.Fatalf("resumed call = %+v, %v", completed, err)
	}
	if text, ok := completed.Output.Text(); !ok || text != `{"id":9007199254740993}:9007199254740993` {
		t.Fatalf("completed output = %q, text = %t", text, ok)
	}
	if want := []tool.IdempotencyKey{key, key}; !slices.Equal(observed, want) {
		t.Fatalf("execution identities = %q, want %q", observed, want)
	}
}

func TestContractRejectsNonUTF8IdempotencyBeforeExecution(t *testing.T) {
	const invalidUTF8 = "\xff"
	executable := &countingTool{name: "search"}
	binding, err := tool.Bind(executable)
	if err != nil {
		t.Fatal(err)
	}
	_, err = binding.Contract().Prepare(
		tool.Request{Name: "search", Arguments: `{"query":"scope"}`},
		tool.Admission{IdempotencyKey: tool.IdempotencyKey(invalidUTF8)},
	)
	if !errors.Is(err, tool.ErrInvalidInvocation) || executable.calls.Load() != 0 {
		t.Fatalf("invalid admission = %v, executions = %d", err, executable.calls.Load())
	}
}
