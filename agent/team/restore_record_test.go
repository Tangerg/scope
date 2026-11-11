package team

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"testing"

	"github.com/Tangerg/scope/agent/internal/conformancetest"
	"github.com/Tangerg/scope/agent/internal/strategy/child"
	"github.com/Tangerg/scope/agent/run"
)

func pendingTaskRecord(t testing.TB) (run.Definition, run.ExecutionState) {
	t.Helper()
	runtime, config := validationPrograms(t)
	definition := require(Compile[string, string](runtime.Compiler(), config)).Definition()
	worker := config.Workers[0].Program.Descriptor().Name()
	state := executionState{
		WorkingState: input("working"), TurnNumber: 1,
		Tasks: []taskState{{Request: request("first", worker, "a"), Invocation: child.Invocation{Request: new(testTurn(1).EffectID(0))}}},
	}
	return definition, require(run.EncodeExecutionState(stateKind, state))
}

func withoutTaskInvocation(t testing.TB, encoded run.ExecutionState) run.ExecutionState {
	t.Helper()
	var record map[string]any
	if err := jsonv2.Unmarshal(encoded.Payload().JSON(), &record); err != nil {
		t.Fatal(err)
	}
	delete(record["tasks"].([]any)[0].(map[string]any), "invocation")
	return require(run.NewExecutionState(stateKind, require(run.EncodePayload(record))))
}

// An absent member never reaches its type's decoder, so a task record without
// its invocation must still be refused.
func TestTeamRestoreRejectsATaskWithoutItsInvocation(t *testing.T) {
	definition, encoded := pendingTaskRecord(t)
	if _, err := definition.Restore(t.Context(), encoded); err != nil {
		t.Fatalf("valid task rejected: %v", err)
	}
	if _, err := definition.Restore(t.Context(), withoutTaskInvocation(t, encoded)); !errors.Is(err, ErrInvalidExecutionState) {
		t.Fatalf("task without invocation admitted: %v", err)
	}
}

func FuzzTeamRestoreRoundTrip(f *testing.F) {
	definition, encoded := pendingTaskRecord(f)
	conformancetest.FuzzRestoreRoundTrip(f, definition, stateKind, encoded, withoutTaskInvocation(f, encoded))
}
