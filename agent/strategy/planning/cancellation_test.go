package planning

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	agent "github.com/Tangerg/scope/agent"
)

func cancellationDefinition(t *testing.T, planner Planner) *Definition {
	t.Helper()
	schema, err := agent.SchemaFor[struct{}]()
	if err != nil {
		t.Fatal(err)
	}
	condition, err := NewCondition("world.done", True)
	if err != nil {
		t.Fatal(err)
	}
	goal, err := NewGoal(GoalConfig{Name: "goal.done", Description: "Finish work.", Conditions: []Condition{condition}})
	if err != nil {
		t.Fatal(err)
	}
	action, err := NewAction(ActionConfig{Name: "action.finish", Description: "Finish work.", Effects: []Condition{condition}})
	if err != nil {
		t.Fatal(err)
	}
	binding, err := NewDispatcherBinding(DispatcherBindingConfig{Action: action})
	if err != nil {
		t.Fatal(err)
	}
	definition, err := NewDefinition(DefinitionConfig{
		Name: "planning.cancellation", Description: "Honor cancellation.", InputSchema: schema, Goal: goal, Actions: []ActionBinding{binding},
		Planner: planner, MaxActionAttempts: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	return definition
}

func TestCanceledStepDoesNotAdvancePlanning(t *testing.T) {
	definition := cancellationDefinition(t, PlannerFunc(func(context.Context, Problem) (Plan, bool, error) {
		t.Fatal("canceled Step called Planner")
		return Plan{}, false, nil
	}))
	for _, current := range []phase{phaseReadySense, phaseAwaitingSense, phaseAwaitingAction} {
		t.Run(string(current), func(t *testing.T) {
			state := executionState{Phase: current, Input: json.RawMessage(`{}`)}
			if current == phaseAwaitingAction {
				state.PlanningPasses = 1
				state.CurrentActionName = "action.finish"
			}
			before, err := encodeExecutionState(state)
			if err != nil {
				t.Fatal(err)
			}
			execution, err := definition.Restore(before)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			transition, err := execution.Step(ctx, nil)
			if !errors.Is(err, context.Canceled) || transition.Valid() {
				t.Fatalf("canceled Step = %+v, %v", transition, err)
			}
			after, err := execution.Snapshot()
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before.Payload(), after.Payload()) {
				t.Fatal("canceled Step changed state")
			}
		})
	}
}

func TestPlannerCancellationRemainsAnError(t *testing.T) {
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		t.Run(cause.Error(), func(t *testing.T) {
			definition := cancellationDefinition(t, PlannerFunc(func(context.Context, Problem) (Plan, bool, error) {
				return Plan{}, false, fmt.Errorf("search interrupted: %w", cause)
			}))
			state, err := encodeExecutionState(executionState{Phase: phaseAwaitingSense, Input: json.RawMessage(`{}`)})
			if err != nil {
				t.Fatal(err)
			}
			execution, err := definition.Restore(state)
			if err != nil {
				t.Fatal(err)
			}
			payload, err := senseSignal(WorldState{}, nil)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(struct {
				ID      string          `json:"id"`
				Payload json.RawMessage `json:"payload"`
			}{ID: "signal:sense", Payload: payload})
			if err != nil {
				t.Fatal(err)
			}
			var signal agent.Signal
			if err = json.Unmarshal(raw, &signal); err != nil {
				t.Fatal(err)
			}
			transition, err := execution.Step(t.Context(), []agent.Signal{signal})
			if !errors.Is(err, cause) || transition.Valid() {
				t.Fatalf("planner cancellation became a domain outcome: %+v, %v", transition, err)
			}
		})
	}
}
