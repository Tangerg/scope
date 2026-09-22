package planning

import (
	"bytes"
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"testing"
	"testing/synctest"
	"time"

	agent "github.com/Tangerg/scope/agent"
)

func cancellationDefinition(t *testing.T, planner Planner, cost CostFunc) *Definition {
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
	action, err := NewAction(ActionConfig{Name: "action.finish", Description: "Finish work.", Effects: []Condition{condition}, Cost: cost})
	if err != nil {
		t.Fatal(err)
	}
	binding, err := NewDispatcherBinding(DispatcherBindingConfig{Action: action})
	if err != nil {
		t.Fatal(err)
	}
	definition, err := NewDefinition(DefinitionConfig{
		Name: "planning.cancellation", Description: "Honor cancellation.", InputSchema: schema, Goal: goal, Actions: []ActionBinding{binding},
		Planner: planner, MaxActionAttempts: agent.NewQuota(4),
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
	}), nil)
	for _, current := range []phase{phaseReadySense, phaseAwaitingSense, phaseAwaitingAction} {
		t.Run(string(current), func(t *testing.T) {
			state := executionState{Phase: current, Input: json.RawMessage(`{}`)}
			if current == phaseAwaitingAction {
				state.PlanningPasses = 1
				state.CurrentActionName = "action.finish"
			}
			before, err := state.snapshot()
			if err != nil {
				t.Fatal(err)
			}
			execution, err := definition.Restore(t.Context(), before)
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
			}), nil)
			state, err := (executionState{Phase: phaseAwaitingSense, Input: json.RawMessage(`{}`)}).snapshot()
			if err != nil {
				t.Fatal(err)
			}
			execution, err := definition.Restore(t.Context(), state)
			if err != nil {
				t.Fatal(err)
			}
			payload, err := senseSignal(WorldState{}, nil)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := jsonv2.Marshal(struct {
				ID      string          `json:"id"`
				Payload json.RawMessage `json:"payload"`
			}{ID: "signal:engine:sense", Payload: payload})
			if err != nil {
				t.Fatal(err)
			}
			var signal agent.Signal
			if err = jsonv2.Unmarshal(raw, &signal); err != nil {
				t.Fatal(err)
			}
			transition, err := execution.Step(t.Context(), []agent.Signal{signal})
			if !errors.Is(err, cause) || transition.Valid() {
				t.Fatalf("planner cancellation became a domain outcome: %+v, %v", transition, err)
			}
		})
	}
}

func TestManagedPlanValidationCancellation(t *testing.T) {
	for _, test := range []struct {
		name      string
		cause     error
		totalCost float64
	}{
		{name: "valid", totalCost: 2},
		{name: "canceled", cause: context.Canceled, totalCost: 2},
		{name: "deadline", cause: context.DeadlineExceeded, totalCost: 2},
		{name: "invalid cost", totalCost: 3},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				if errors.Is(test.cause, context.DeadlineExceeded) {
					cancel()
					ctx, cancel = context.WithTimeout(t.Context(), time.Second)
				}
				defer cancel()
				enteredCost := make(chan struct{})
				if errors.Is(test.cause, context.Canceled) {
					go func() {
						<-enteredCost
						cancel()
					}()
				}
				planned, err := NewPlannedAction("action.finish")
				if err != nil {
					t.Fatal(err)
				}
				plan, err := NewPlan([]PlannedAction{planned, planned}, test.totalCost)
				if err != nil {
					t.Fatal(err)
				}
				calls := 0
				definition := cancellationDefinition(t, PlannerFunc(func(context.Context, Problem) (Plan, bool, error) {
					return plan, true, nil
				}), func(WorldState) (float64, error) {
					calls++
					if calls == 1 && test.cause != nil {
						close(enteredCost)
						<-ctx.Done()
					}
					return 1, nil
				})
				state, err := (executionState{Phase: phaseAwaitingSense, Input: json.RawMessage(`{}`)}).snapshot()
				if err != nil {
					t.Fatal(err)
				}
				execution, err := definition.Restore(t.Context(), state)
				if err != nil {
					t.Fatal(err)
				}
				payload, err := senseSignal(WorldState{}, nil)
				if err != nil {
					t.Fatal(err)
				}
				var signal agent.Signal
				if err = jsonv2.Unmarshal([]byte(`{"id":"signal:engine:sense","payload":`+string(payload)+`}`), &signal); err != nil {
					t.Fatal(err)
				}
				transition, err := execution.Step(ctx, []agent.Signal{signal})
				if test.cause != nil {
					if !errors.Is(err, test.cause) || transition.Valid() || calls != 1 {
						t.Fatalf("canceled validation: transition=%+v, error=%v, Cost calls=%d", transition, err, calls)
					}
					return
				}
				if err != nil || !transition.Valid() || calls != 2 {
					t.Fatalf("validation: transition=%+v, error=%v, Cost calls=%d", transition, err, calls)
				}
				if test.totalCost != 2 {
					failure, failed := transition.Failure()
					if !failed || failure.Kind() != agent.FailureKindContract || failure.Code() != failureCodePlanningPlannerContract {
						t.Fatalf("invalid plan failure = %+v", transition)
					}
					return
				}
				if transition.Kind() != agent.TransitionKindContinue || len(transition.Effects()) != 1 {
					t.Fatalf("valid plan did not request its first Action: %+v", transition)
				}
			})
		})
	}
}
