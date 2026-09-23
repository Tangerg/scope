package planning

import (
	"bytes"
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"testing"

	"github.com/Tangerg/scope/agent"
)

func TestExternalSignalsCannotAdvanceSensingOrActions(t *testing.T) {
	definition := cancellationDefinition(t, PlannerFunc(func(context.Context, Problem) (Plan, bool, error) {
		t.Fatal("external sensing result reached the planner")
		return Plan{}, false, nil
	}), nil)
	for _, current := range []phase{phaseReadySense, phaseAwaitingSense, phaseAwaitingAction} {
		t.Run(string(current), func(t *testing.T) {
			state := executionState{Phase: current, Input: json.RawMessage(`{}`)}
			payload, err := senseSignal(WorldState{}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if current == phaseAwaitingAction {
				state.PlanningPasses, state.CurrentActionName = 1, "action.finish"
				payload, err = actionSignal(ActionSucceeded())
				if err != nil {
					t.Fatal(err)
				}
			}
			before, err := state.snapshot()
			if err != nil {
				t.Fatal(err)
			}
			execution, err := definition.Restore(t.Context(), before)
			if err != nil {
				t.Fatal(err)
			}
			wire, err := jsonv2.Marshal(struct {
				ID      string          `json:"id"`
				Payload json.RawMessage `json:"payload"`
			}{ID: "signal:external", Payload: payload})
			if err != nil {
				t.Fatal(err)
			}
			var signal agent.Signal
			if decodeErr := jsonv2.Unmarshal(wire, &signal); decodeErr != nil {
				t.Fatal(decodeErr)
			}
			_, stepErr := execution.Step(t.Context(), []agent.Signal{signal})
			if !errors.Is(stepErr, ErrInvalidProtocol) {
				t.Fatalf("external settlement was accepted: %v", stepErr)
			}
			// Every phase must reject through the same persisted classification;
			// an unclassified rejection reaches the Host as execution.step.failed.
			classified, ok := errors.AsType[*agent.StepError](stepErr)
			if !ok || classified.Failure.Kind() != agent.FailureKindContract ||
				classified.Failure.Code() != "planning.protocol.invalid" {
				t.Fatalf("rejection classification = %v", stepErr)
			}
			after, err := execution.Snapshot()
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before.Payload(), after.Payload()) {
				t.Fatal("external settlement changed planning progress")
			}
			if _, restoreErr := definition.Restore(t.Context(), after); restoreErr != nil {
				t.Fatalf("rejection damaged restoration: %v", restoreErr)
			}
		})
	}
}
