package workflow

import (
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"testing"

	"github.com/Tangerg/scope/agent/internal/conformancetest"
	"github.com/Tangerg/scope/agent/internal/strategy/child"
	"github.com/Tangerg/scope/agent/run"
)

func singleChildRecord(t testing.TB) (*definition, executionState) {
	t.Helper()
	leaf := mustProgram(t, mustDefinition(t, "test.leaf", mustTransform(t, "identity", func(_ context.Context, value int) (int, error) { return value, nil })), "leaf")
	stage, err := Call(CallConfig{ID: "call", Child: run.ChildBinding{Program: leaf}})
	if err != nil {
		t.Fatal(err)
	}
	state := executionState{CurrentValue: mustPayload(t, 1), Child: &singleChild{Invocation: child.Invocation{Request: new(testTurn(1).EffectID(0))}}}
	return mustDefinition(t, "test.call", stage), state
}

func withoutChildInvocation(t testing.TB, encoded run.ExecutionState) run.ExecutionState {
	t.Helper()
	var record map[string]any
	if err := jsonv2.Unmarshal(encoded.Payload().JSON(), &record); err != nil {
		t.Fatal(err)
	}
	delete(record["child"].(map[string]any), "invocation")
	payload, err := run.EncodePayload(record)
	if err != nil {
		t.Fatal(err)
	}
	stripped, err := run.NewExecutionState(executionStateKind, payload)
	if err != nil {
		t.Fatal(err)
	}
	return stripped
}

// An absent member never reaches its type's decoder, so a single child without
// its invocation must still be refused.
func TestRestoreRejectsASingleChildWithoutItsInvocation(t *testing.T) {
	definition, state := singleChildRecord(t)
	encoded, err := state.snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := definition.Restore(t.Context(), encoded); err != nil {
		t.Fatalf("valid single child rejected: %v", err)
	}
	if _, err := definition.Restore(t.Context(), withoutChildInvocation(t, encoded)); !errors.Is(err, ErrInvalidExecutionState) {
		t.Fatalf("single child without invocation admitted: %v", err)
	}
}

func FuzzWorkflowRestoreRoundTrip(f *testing.F) {
	definition, state := singleChildRecord(f)
	encoded, err := state.snapshot()
	if err != nil {
		f.Fatal(err)
	}
	conformancetest.FuzzRestoreRoundTrip(f, definition, executionStateKind, encoded, withoutChildInvocation(f, encoded))
}
