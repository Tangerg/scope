package workflow

import (
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"testing"

	"github.com/Tangerg/scope/agent/internal/strategy/child"
	"github.com/Tangerg/scope/agent/run"
)

// An absent member never reaches its type's decoder, so a single child without
// its invocation must still be refused.
func TestRestoreRejectsASingleChildWithoutItsInvocation(t *testing.T) {
	leaf := mustProgram(t, mustDefinition(t, "test.leaf", mustTransform(t, "identity", func(_ context.Context, value int) (int, error) { return value, nil })), "leaf")
	stage, err := Call(CallConfig{ID: "call", Child: run.ChildBinding{Program: leaf}})
	if err != nil {
		t.Fatal(err)
	}
	definition := mustDefinition(t, "test.call", stage)
	state := executionState{CurrentValue: mustPayload(t, 1), Child: &singleChild{Invocation: child.Invocation{Request: new(testTurn(1).EffectID(0))}}}
	encoded, err := state.snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := definition.Restore(t.Context(), encoded); err != nil {
		t.Fatalf("valid single child rejected: %v", err)
	}
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
	if _, err := definition.Restore(t.Context(), stripped); !errors.Is(err, ErrInvalidExecutionState) {
		t.Fatalf("single child without invocation admitted: %v", err)
	}
}
