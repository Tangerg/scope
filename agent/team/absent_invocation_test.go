package team

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"testing"

	"github.com/Tangerg/scope/agent/internal/strategy/child"
	"github.com/Tangerg/scope/agent/run"
)

// An absent member never reaches its type's decoder, so a task record without
// its invocation must still be refused.
func TestTeamRestoreRejectsATaskWithoutItsInvocation(t *testing.T) {
	runtime, config := validationPrograms(t)
	definition := require(Compile[string, string](runtime.Compiler(), config)).Definition()
	worker := config.Workers[0].Program.Descriptor().Name()
	state := executionState{
		WorkingState: input("working"), TurnNumber: 1,
		Tasks: []taskState{{Request: request("first", worker, "a"), Invocation: child.Invocation{Request: new(testTurn(1).EffectID(0))}}},
	}
	encoded := require(run.EncodeExecutionState(stateKind, state))
	if _, err := definition.Restore(t.Context(), encoded); err != nil {
		t.Fatalf("valid task rejected: %v", err)
	}
	var record map[string]any
	if err := jsonv2.Unmarshal(encoded.Payload().JSON(), &record); err != nil {
		t.Fatal(err)
	}
	delete(record["tasks"].([]any)[0].(map[string]any), "invocation")
	stripped := require(run.NewExecutionState(stateKind, require(run.EncodePayload(record))))
	if _, err := definition.Restore(t.Context(), stripped); !errors.Is(err, ErrInvalidExecutionState) {
		t.Fatalf("task without invocation admitted: %v", err)
	}
}
