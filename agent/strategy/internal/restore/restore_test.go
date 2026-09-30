package restore_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/strategy/internal/restore"
)

var errInvalidState = errors.New("invalid state")

type testState struct {
	Value string `json:"value"`
}

func encodedState(t *testing.T) agent.ExecutionState {
	t.Helper()
	state, err := agent.EncodeExecutionState("test.state", testState{Value: "ok"})
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func TestDecodeReturnsValidatedState(t *testing.T) {
	var validated testState
	decoded, err := restore.Decode(t.Context(), encodedState(t), "test.state", errInvalidState,
		func(_ context.Context, state testState) error {
			validated = state
			return nil
		})
	if err != nil || decoded != (testState{Value: "ok"}) || validated != decoded {
		t.Fatalf("Decode = %+v, %v; validated %+v", decoded, err, validated)
	}
}

func TestDecodeRejectsAnotherKindAsInvalidState(t *testing.T) {
	_, err := restore.Decode(t.Context(), encodedState(t), "other.state", errInvalidState,
		func(context.Context, testState) error { t.Fatal("validated a mismatched kind"); return nil })
	if !errors.Is(err, errInvalidState) {
		t.Fatalf("Decode error = %v, want %v", err, errInvalidState)
	}
}

func TestDecodeReturnsValidationErrorUnchanged(t *testing.T) {
	errInconsistent := errors.New("inconsistent")
	_, err := restore.Decode(t.Context(), encodedState(t), "test.state", errInvalidState,
		func(context.Context, testState) error { return errInconsistent })
	if !errors.Is(err, errInconsistent) || errors.Is(err, errInvalidState) {
		t.Fatalf("Decode error = %v, want only %v", err, errInconsistent)
	}
}

func TestDecodeHonorsCanceledContextBeforeValidation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := restore.Decode(ctx, encodedState(t), "test.state", errInvalidState,
		func(context.Context, testState) error { t.Fatal("validated after cancellation"); return nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Decode error = %v, want %v", err, context.Canceled)
	}
}

func TestDecodeReportsCancellationDuringValidation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	_, err := restore.Decode(ctx, encodedState(t), "test.state", errInvalidState,
		func(context.Context, testState) error { cancel(); return nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Decode error = %v, want %v", err, context.Canceled)
	}
}
