package planning_test

import (
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"math"
	"slices"
	"testing"

	"github.com/Tangerg/scope/agent/strategy/planning"
)

func TestWorldStatePreservesThreeValuedImmutableFacts(t *testing.T) {
	ready := mustCondition(t, "world.ready", planning.TruthTrue)
	disabled := mustCondition(t, "world.disabled", planning.TruthFalse)
	input := []planning.Condition{ready, disabled}
	state := mustWorldState(t, input...)
	input[0] = mustCondition(t, "world.replaced", planning.TruthTrue)

	if state.Truth("world.ready") != planning.TruthTrue || state.Truth("world.disabled") != planning.TruthFalse ||
		state.Truth("world.missing") != planning.TruthUnknown {
		t.Fatalf("unexpected truth projection: %#v", state.Conditions())
	}
	conditions := state.Conditions()
	if got := []string{conditions[0].Key(), conditions[1].Key()}; !slices.Equal(got, []string{"world.disabled", "world.ready"}) {
		t.Fatalf("condition order = %v", got)
	}
	conditions[0] = mustCondition(t, "world.mutated", planning.TruthTrue)
	if state.Truth("world.disabled") != planning.TruthFalse {
		t.Fatal("Conditions exposed mutable storage")
	}

	updated, err := state.Apply(mustCondition(t, "world.ready", planning.TruthFalse))
	if err != nil {
		t.Fatal(err)
	}
	if state.Truth("world.ready") != planning.TruthTrue || updated.Truth("world.ready") != planning.TruthFalse ||
		state.Key() == updated.Key() {
		t.Fatalf("immutable apply failed: before=%s after=%s", state.Key(), updated.Key())
	}
}

func TestWorldStateApplyOrdersFactsAndUsesLastRepeatedEffect(t *testing.T) {
	state := mustWorldState(t,
		mustCondition(t, "b", planning.TruthFalse),
		mustCondition(t, "d", planning.TruthTrue),
	)
	effects := []planning.Condition{
		mustCondition(t, "e", planning.TruthTrue),
		mustCondition(t, "b", planning.TruthTrue),
		mustCondition(t, "a", planning.TruthFalse),
		mustCondition(t, "e", planning.TruthFalse),
		mustCondition(t, "c", planning.TruthTrue),
	}
	before := slices.Clone(effects)
	updated, err := state.Apply(effects...)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Key() != "a=0|b=1|c=1|d=1|e=0|" || state.Key() != "b=0|d=1|" ||
		!slices.Equal(effects, before) {
		t.Fatalf("apply changed its inputs or facts: %s -> %s", state.Key(), updated.Key())
	}
	effects[0] = mustCondition(t, "z", planning.TruthTrue)
	if updated.Truth("z") != planning.TruthUnknown {
		t.Fatal("successor retained caller effects")
	}
	if unchanged, applyErr := state.Apply(); applyErr != nil || unchanged.Key() != state.Key() {
		t.Fatalf("empty effects changed the state: %s, %v", unchanged.Key(), applyErr)
	}
	if _, applyErr := state.Apply(planning.Condition{}); !errors.Is(applyErr, planning.ErrInvalidWorldState) {
		t.Fatalf("invalid effect accepted: %v", applyErr)
	}
}

func TestPlanningValuesUseStrictPortableJSON(t *testing.T) {
	state := mustWorldState(t,
		mustCondition(t, "world.alpha", planning.TruthTrue),
		mustCondition(t, "world.beta", planning.TruthFalse),
	)
	data, err := jsonv2.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	var restored planning.WorldState
	if err := jsonv2.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}
	if restored.Key() != state.Key() {
		t.Fatalf("restored key = %q, want %q", restored.Key(), state.Key())
	}
	for _, invalid := range []string{`null`, `{}`, `{"conditions":null}`, `{"conditions":[],"extra":true}`} {
		if err := jsonv2.Unmarshal([]byte(invalid), &restored); !errors.Is(err, planning.ErrInvalidWorldState) {
			t.Fatalf("invalid WorldState %s error = %v", invalid, err)
		}
		if restored.Key() != state.Key() {
			t.Fatal("invalid WorldState replaced the last complete observation")
		}
	}
	var truth planning.Truth
	if err := truth.UnmarshalJSON([]byte(`"true" false`)); !errors.Is(err, planning.ErrInvalidCondition) {
		t.Fatalf("trailing Truth error = %v", err)
	}
}

func TestGoalAndActionOwnConstructionInputs(t *testing.T) {
	required := []planning.Condition{mustCondition(t, "world.done", planning.TruthTrue)}
	goal, err := planning.NewGoal(planning.GoalConfig{
		Name: "goal.done", Description: "Make the world done.", Conditions: required,
	})
	if err != nil {
		t.Fatal(err)
	}
	required[0] = mustCondition(t, "world.changed", planning.TruthTrue)
	if goal.Conditions()[0].Key() != "world.done" {
		t.Fatal("Goal retained caller slice")
	}

	preconditions := []planning.Condition{mustCondition(t, "world.ready", planning.TruthTrue)}
	effects := []planning.Condition{mustCondition(t, "world.done", planning.TruthTrue)}
	action := mustAction(t, planning.ActionConfig{
		Name: "action.finish", Description: "Finish the work.",
		Preconditions: preconditions, Effects: effects,
	})
	preconditions[0] = mustCondition(t, "world.changed", planning.TruthTrue)
	effects[0] = mustCondition(t, "world.changed", planning.TruthFalse)
	if action.Preconditions()[0].Key() != "world.ready" || action.Effects()[0].Key() != "world.done" {
		t.Fatal("Action retained caller slices")
	}
	cost, err := action.Cost(mustWorldState(t, mustCondition(t, "world.ready", planning.TruthTrue)))
	if err != nil || cost != 1 {
		t.Fatalf("default cost = %v, error = %v", cost, err)
	}
}

func TestActionRejectsPredictiveNoOpAndInvalidCosts(t *testing.T) {
	ready := mustCondition(t, "world.ready", planning.TruthTrue)
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
				Effects: []planning.Condition{mustCondition(t, "world.done", planning.TruthTrue)}, Cost: test.cost,
			})
			_, err := action.Cost(planning.WorldState{})
			if !errors.Is(err, planning.ErrInvalidActionCost) || !errors.Is(err, test.want) {
				t.Fatalf("Cost error = %v", err)
			}
		})
	}
}

func TestProblemValidatesPlannerOutputAgainstItsActions(t *testing.T) {
	ready := mustCondition(t, "world.ready", planning.TruthTrue)
	done := mustCondition(t, "world.done", planning.TruthTrue)
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
	valid, err := planning.NewPlan([]planning.PlannedAction{planned})
	if err != nil {
		t.Fatal(err)
	}
	if cost, evaluationErr := problem.EvaluatePlan(t.Context(), valid); evaluationErr != nil || cost != 2 {
		t.Fatalf("evaluated cost = %v, error = %v", cost, evaluationErr)
	}
	if data := mustJSON(t, valid); string(data) != `{"actions":["action.finish"]}` {
		t.Fatalf("Plan repeats predictive metadata: %s", data)
	}
	var retired planning.Plan
	if decodeErr := jsonv2.Unmarshal([]byte(`{"actions":["action.finish"],"total_cost":2}`), &retired); !errors.Is(decodeErr, planning.ErrInvalidPlan) {
		t.Fatalf("retired cost owner accepted: %v", decodeErr)
	}
	unknown, _ := planning.NewPlannedAction("action.unknown")
	unknownPlan, _ := planning.NewPlan([]planning.PlannedAction{unknown})
	if _, err := problem.EvaluatePlan(t.Context(), unknownPlan); !errors.Is(err, planning.ErrInvalidPlan) {
		t.Fatalf("unknown-Action error = %v", err)
	}
}

func TestOutputValidatesCompletedPlanningFacts(t *testing.T) {
	done := mustCondition(t, "world.done", planning.TruthTrue)
	succeeded := planning.Attempt{ActionName: "action.finish", Status: planning.AttemptSucceeded}
	failed := planning.Attempt{ActionName: "action.finish", Status: planning.AttemptFailed, Diagnostic: "refused"}
	unconfirmed := planning.Attempt{ActionName: "action.finish", Status: planning.AttemptUnconfirmed}
	for _, test := range []struct {
		name     string
		outcome  planning.Outcome
		attempts []planning.Attempt
		passes   uint64
		valid    bool
	}{
		{name: "already achieved", outcome: planning.OutcomeAchieved, valid: true},
		{name: "achieved after action", outcome: planning.OutcomeAchieved, attempts: []planning.Attempt{succeeded}, passes: 1, valid: true},
		{name: "unreachable", outcome: planning.OutcomeUnreachable, passes: 1, valid: true},
		{name: "unreachable after attempt", outcome: planning.OutcomeUnreachable, attempts: []planning.Attempt{failed}},
		{name: "attempt limit exhausted", outcome: planning.OutcomeExhausted, attempts: []planning.Attempt{failed}, passes: 1, valid: true},
		{name: "exhausted without attempts", outcome: planning.OutcomeExhausted},
		{name: "replanning stuck", outcome: planning.OutcomeStuck, attempts: []planning.Attempt{failed}, passes: 2, valid: true},
		{name: "stuck without attempts", outcome: planning.OutcomeStuck},
		{name: "repeated success", outcome: planning.OutcomeAchieved, attempts: []planning.Attempt{succeeded, succeeded}, passes: 2, valid: true},
		{name: "attempt after failure", outcome: planning.OutcomeAchieved, attempts: []planning.Attempt{failed, succeeded}, passes: 2, valid: true},
		{name: "attempt after unconfirmed action", outcome: planning.OutcomeStuck, attempts: []planning.Attempt{unconfirmed, succeeded}, passes: 3, valid: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			output := planning.Output{Outcome: test.outcome, Attempts: test.attempts}
			if test.outcome == planning.OutcomeAchieved {
				output.WorldState = mustWorldState(t, done)
			}
			if err := output.Validate(); (err == nil) != test.valid || !test.valid && !errors.Is(err, planning.ErrInvalidResult) {
				t.Fatalf("Validate = %v, want valid=%t", err, test.valid)
			}
			if test.valid && output.PlanningPasses() != test.passes {
				t.Fatalf("PlanningPasses = %d, want %d", output.PlanningPasses(), test.passes)
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

func TestAttemptAndOutputValidationShareResultClassification(t *testing.T) {
	for _, attempt := range []planning.Attempt{
		{},
		{ActionName: "action", Status: planning.AttemptSucceeded, Diagnostic: "unexpected"},
		{ActionName: "action", Status: planning.AttemptFailed},
	} {
		if err := attempt.Validate(); !errors.Is(err, planning.ErrInvalidResult) {
			t.Fatalf("attempt classification lost: %v", err)
		}
		if err := (planning.Output{Outcome: planning.OutcomeAchieved, Attempts: []planning.Attempt{attempt}}).Validate(); !errors.Is(err, planning.ErrInvalidResult) {
			t.Fatalf("nested attempt classification lost: %v", err)
		}
	}
	if err := (planning.Output{}).Validate(); !errors.Is(err, planning.ErrInvalidResult) {
		t.Fatalf("output classification lost: %v", err)
	}
}

func TestEvaluatePlanStopsAfterCanceledCost(t *testing.T) {
	for _, count := range []int{1, 2} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			calls := 0
			done := mustCondition(t, "world.done", planning.TruthTrue)
			action := mustAction(t, planning.ActionConfig{Name: "action.finish", Description: "Finish work.", Effects: []planning.Condition{done}, Cost: func(planning.WorldState) (float64, error) {
				calls++
				cancel()
				return 1, nil
			}})
			problem, err := planning.NewProblem(planning.WorldState{}, mustGoal(t, done), action)
			if err != nil {
				t.Fatal(err)
			}
			planned, _ := planning.NewPlannedAction("action.finish")
			actions := make([]planning.PlannedAction, count)
			for i := range actions {
				actions[i] = planned
			}
			plan, err := planning.NewPlan(actions)
			if err != nil {
				t.Fatal(err)
			}
			_, err = problem.EvaluatePlan(ctx, plan)
			if !errors.Is(err, context.Canceled) || calls != 1 {
				t.Fatalf("error=%v calls=%d", err, calls)
			}
		})
	}
}
