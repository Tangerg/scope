package planning_test

import (
	"encoding/json"
	"errors"
	"math"
	"slices"
	"testing"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/planning"
)

func TestWorldStatePreservesThreeValuedImmutableFacts(t *testing.T) {
	ready := mustCondition(t, "world.ready", planning.True)
	disabled := mustCondition(t, "world.disabled", planning.False)
	input := []planning.Condition{ready, disabled}
	state := mustWorldState(t, input...)
	input[0] = mustCondition(t, "world.replaced", planning.True)

	if state.Truth("world.ready") != planning.True || state.Truth("world.disabled") != planning.False ||
		state.Truth("world.missing") != planning.Unknown {
		t.Fatalf("unexpected truth projection: %#v", state.Conditions())
	}
	conditions := state.Conditions()
	if got := []string{conditions[0].Key(), conditions[1].Key()}; !slices.Equal(got, []string{"world.disabled", "world.ready"}) {
		t.Fatalf("condition order = %v", got)
	}
	conditions[0] = mustCondition(t, "world.mutated", planning.True)
	if state.Truth("world.disabled") != planning.False {
		t.Fatal("Conditions exposed mutable storage")
	}

	updated, err := state.Apply(mustCondition(t, "world.ready", planning.False))
	if err != nil {
		t.Fatal(err)
	}
	if state.Truth("world.ready") != planning.True || updated.Truth("world.ready") != planning.False ||
		state.Key() == updated.Key() {
		t.Fatalf("immutable apply failed: before=%s after=%s", state.Key(), updated.Key())
	}
}

func TestPlanningValuesUseStrictPortableJSON(t *testing.T) {
	state := mustWorldState(t,
		mustCondition(t, "world.alpha", planning.True),
		mustCondition(t, "world.beta", planning.False),
	)
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	var restored planning.WorldState
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}
	if restored.Key() != state.Key() {
		t.Fatalf("restored key = %q, want %q", restored.Key(), state.Key())
	}
	if err := json.Unmarshal([]byte(`{"conditions":[],"extra":true}`), &restored); !errors.Is(err, planning.ErrInvalidWorldState) {
		t.Fatalf("unknown-field error = %v", err)
	}
	var truth planning.Truth
	if err := truth.UnmarshalJSON([]byte(`"true" false`)); !errors.Is(err, planning.ErrInvalidCondition) {
		t.Fatalf("trailing Truth error = %v", err)
	}
}

func TestGoalAndActionOwnConstructionInputs(t *testing.T) {
	required := []planning.Condition{mustCondition(t, "world.done", planning.True)}
	goal, err := planning.NewGoal(planning.GoalConfig{
		Name: "goal.done", Description: "Make the world done.", Conditions: required,
	})
	if err != nil {
		t.Fatal(err)
	}
	required[0] = mustCondition(t, "world.changed", planning.True)
	if goal.Conditions()[0].Key() != "world.done" {
		t.Fatal("Goal retained caller slice")
	}

	preconditions := []planning.Condition{mustCondition(t, "world.ready", planning.True)}
	effects := []planning.Condition{mustCondition(t, "world.done", planning.True)}
	action := mustAction(t, planning.ActionConfig{
		Name: "action.finish", Description: "Finish the work.",
		Preconditions: preconditions, Effects: effects,
	})
	preconditions[0] = mustCondition(t, "world.changed", planning.True)
	effects[0] = mustCondition(t, "world.changed", planning.False)
	if action.Preconditions()[0].Key() != "world.ready" || action.Effects()[0].Key() != "world.done" {
		t.Fatal("Action retained caller slices")
	}
	cost, err := action.Cost(mustWorldState(t, mustCondition(t, "world.ready", planning.True)))
	if err != nil || cost != 1 {
		t.Fatalf("default cost = %v, error = %v", cost, err)
	}
}

func TestActionRejectsPredictiveNoOpAndInvalidCosts(t *testing.T) {
	ready := mustCondition(t, "world.ready", planning.True)
	_, err := planning.NewAction(planning.ActionConfig{
		Name: "action.noop", Description: "Predict no state change.",
		Preconditions: []planning.Condition{ready}, Effects: []planning.Condition{ready},
	})
	if !errors.Is(err, planning.ErrInvalidAction) {
		t.Fatalf("no-op error = %v", err)
	}

	cause := errors.New("cost sentinel")
	tests := []struct {
		name string
		cost planning.CostFunc
		want error
	}{
		{name: "negative", cost: planning.FixedCost(-1), want: planning.ErrInvalidActionCost},
		{name: "nan", cost: planning.FixedCost(math.NaN()), want: planning.ErrInvalidActionCost},
		{name: "infinite", cost: planning.FixedCost(math.Inf(1)), want: planning.ErrInvalidActionCost},
		{name: "error", cost: func(planning.WorldState) (float64, error) { return 0, cause }, want: cause},
		{name: "panic", cost: func(planning.WorldState) (float64, error) { panic(cause) }, want: cause},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			action := mustAction(t, planning.ActionConfig{
				Name: "action.cost", Description: "Evaluate one test cost.",
				Effects: []planning.Condition{mustCondition(t, "world.done", planning.True)}, Cost: test.cost,
			})
			_, err := action.Cost(planning.WorldState{})
			if !errors.Is(err, planning.ErrInvalidActionCost) || !errors.Is(err, test.want) {
				t.Fatalf("Cost error = %v", err)
			}
		})
	}
}

func TestProblemValidatesPlannerOutputAgainstItsActions(t *testing.T) {
	ready := mustCondition(t, "world.ready", planning.True)
	done := mustCondition(t, "world.done", planning.True)
	action := mustAction(t, planning.ActionConfig{
		Name: "action.finish", Description: "Finish ready work.",
		Preconditions: []planning.Condition{ready}, Effects: []planning.Condition{done}, Cost: planning.FixedCost(2),
	})
	goal := mustGoal(t, done)
	problem, err := planning.NewProblem(mustWorldState(t, ready), goal, action)
	if err != nil {
		t.Fatal(err)
	}
	planned, err := planning.NewPlannedAction("action.finish")
	if err != nil {
		t.Fatal(err)
	}
	valid, err := planning.NewPlan([]planning.PlannedAction{planned}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := problem.ValidatePlan(valid); err != nil {
		t.Fatal(err)
	}
	wrongCost, _ := planning.NewPlan([]planning.PlannedAction{planned}, 3)
	if err := problem.ValidatePlan(wrongCost); !errors.Is(err, planning.ErrInvalidPlan) {
		t.Fatalf("wrong-cost error = %v", err)
	}
	unknown, _ := planning.NewPlannedAction("action.unknown")
	unknownPlan, _ := planning.NewPlan([]planning.PlannedAction{unknown}, 2)
	if err := problem.ValidatePlan(unknownPlan); !errors.Is(err, planning.ErrInvalidPlan) {
		t.Fatalf("unknown-Action error = %v", err)
	}
}

func TestOutputValidatesCompletedPlanningFacts(t *testing.T) {
	done := mustCondition(t, "world.done", planning.True)
	action := mustAction(t, planning.ActionConfig{
		Name: "action.finish", Description: "Finish the work.", Effects: []planning.Condition{done},
	})
	definition := newManagedDefinition(t, managedDeploymentConfig{
		goal: mustGoal(t, done), bindings: []planning.ActionBinding{mustDispatcherBinding(t, action)},
	})
	succeeded := planning.Attempt{ActionName: "action.finish", Status: planning.AttemptSucceeded}
	failed := planning.Attempt{ActionName: "action.finish", Status: planning.AttemptFailed, Diagnostic: "refused"}
	unconfirmed := planning.Attempt{ActionName: "action.finish", Status: planning.AttemptUnconfirmed, Diagnostic: "not observed"}
	for _, test := range []struct {
		name     string
		outcome  planning.Outcome
		attempts []planning.Attempt
		passes   uint32
		valid    bool
	}{
		{name: "already achieved", outcome: planning.OutcomeAchieved, valid: true},
		{name: "achieved after action", outcome: planning.OutcomeAchieved, attempts: []planning.Attempt{succeeded}, passes: 1, valid: true},
		{name: "achieved with missing pass", outcome: planning.OutcomeAchieved, attempts: []planning.Attempt{succeeded}},
		{name: "achieved with extra pass", outcome: planning.OutcomeAchieved, passes: 1},
		{name: "unreachable", outcome: planning.OutcomeUnreachable, passes: 1, valid: true},
		{name: "unreachable after attempt", outcome: planning.OutcomeUnreachable, attempts: []planning.Attempt{failed}, passes: 1},
		{name: "attempt limit exhausted", outcome: planning.OutcomeStuck, attempts: []planning.Attempt{failed}, passes: 1, valid: true},
		{name: "replanning exhausted", outcome: planning.OutcomeStuck, attempts: []planning.Attempt{failed}, passes: 2, valid: true},
		{name: "stuck with missing pass", outcome: planning.OutcomeStuck, attempts: []planning.Attempt{failed}},
		{name: "stuck with extra passes", outcome: planning.OutcomeStuck, attempts: []planning.Attempt{failed}, passes: 3},
		{name: "repeated success", outcome: planning.OutcomeAchieved, attempts: []planning.Attempt{succeeded, succeeded}, passes: 2, valid: true},
		{name: "attempt after failure", outcome: planning.OutcomeAchieved, attempts: []planning.Attempt{failed, succeeded}, passes: 2},
		{name: "attempt after unconfirmed action", outcome: planning.OutcomeStuck, attempts: []planning.Attempt{unconfirmed, succeeded}, passes: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			output := planning.Output{Outcome: test.outcome, Attempts: test.attempts, PlanningPasses: test.passes}
			if test.outcome == planning.OutcomeAchieved {
				output.WorldState = mustWorldState(t, done)
			}
			if err := output.Validate(); (err == nil) != test.valid {
				t.Fatalf("Validate = %v, want valid=%t", err, test.valid)
			}
			payload := mustJSON(t, struct {
				Phase string          `json:"phase"`
				Input json.RawMessage `json:"input"`
				planning.Output
			}{Phase: "completed", Input: json.RawMessage(`{}`), Output: output})
			state, err := agent.NewExecutionState("planning", payload)
			if err != nil {
				t.Fatal(err)
			}
			_, err = definition.Restore(state)
			if (err == nil) != test.valid || err != nil && !errors.Is(err, planning.ErrInvalidExecutionState) {
				t.Fatalf("Restore = %v, want valid=%t", err, test.valid)
			}
		})
	}
}

func mustCondition(t *testing.T, key string, truth planning.Truth) planning.Condition {
	t.Helper()
	condition, err := planning.NewCondition(key, truth)
	if err != nil {
		t.Fatal(err)
	}
	return condition
}

func mustWorldState(t *testing.T, conditions ...planning.Condition) planning.WorldState {
	t.Helper()
	state, err := planning.NewWorldState(conditions...)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func mustGoal(t *testing.T, conditions ...planning.Condition) planning.Goal {
	t.Helper()
	goal, err := planning.NewGoal(planning.GoalConfig{
		Name: "goal.test", Description: "Reach the test target.", Conditions: conditions,
	})
	if err != nil {
		t.Fatal(err)
	}
	return goal
}

func mustAction(t *testing.T, config planning.ActionConfig) planning.Action {
	t.Helper()
	action, err := planning.NewAction(config)
	if err != nil {
		t.Fatal(err)
	}
	return action
}
