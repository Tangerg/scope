package planning

import (
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"math"
	"slices"
	"testing"
)

func TestWorldStatePreservesThreeValuedImmutableFacts(t *testing.T) {
	ready := mustCondition(t, "world.ready", TruthTrue)
	disabled := mustCondition(t, "world.disabled", TruthFalse)
	input := []Condition{ready, disabled}
	state := mustWorldState(t, input...)
	input[0] = mustCondition(t, "world.replaced", TruthTrue)

	if state.Truth("world.ready") != TruthTrue || state.Truth("world.disabled") != TruthFalse ||
		state.Truth("world.missing") != TruthUnknown {
		t.Fatalf("unexpected truth projection: %#v", state.Conditions())
	}
	conditions := state.Conditions()
	if got := []string{conditions[0].Key(), conditions[1].Key()}; !slices.Equal(got, []string{"world.disabled", "world.ready"}) {
		t.Fatalf("condition order = %v", got)
	}
	conditions[0] = mustCondition(t, "world.mutated", TruthTrue)
	if state.Truth("world.disabled") != TruthFalse {
		t.Fatal("Conditions exposed mutable storage")
	}

	updated, err := state.Apply(mustCondition(t, "world.ready", TruthFalse))
	if err != nil {
		t.Fatal(err)
	}
	if state.Truth("world.ready") != TruthTrue || updated.Truth("world.ready") != TruthFalse ||
		state.Key() == updated.Key() {
		t.Fatalf("immutable apply failed: before=%s after=%s", state.Key(), updated.Key())
	}
}

func TestWorldStateApplyOrdersFactsAndUsesLastRepeatedEffect(t *testing.T) {
	state := mustWorldState(t,
		mustCondition(t, "b", TruthFalse),
		mustCondition(t, "d", TruthTrue),
	)
	effects := []Condition{
		mustCondition(t, "e", TruthTrue),
		mustCondition(t, "b", TruthTrue),
		mustCondition(t, "a", TruthFalse),
		mustCondition(t, "e", TruthFalse),
		mustCondition(t, "c", TruthTrue),
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
	effects[0] = mustCondition(t, "z", TruthTrue)
	if updated.Truth("z") != TruthUnknown {
		t.Fatal("successor retained caller effects")
	}
	if unchanged, applyErr := state.Apply(); applyErr != nil || unchanged.Key() != state.Key() {
		t.Fatalf("empty effects changed the state: %s, %v", unchanged.Key(), applyErr)
	}
	if _, applyErr := state.Apply(Condition{}); !errors.Is(applyErr, ErrInvalidWorldState) {
		t.Fatalf("invalid effect accepted: %v", applyErr)
	}
}

func TestPlanningValuesUseStrictPortableJSON(t *testing.T) {
	state := mustWorldState(t,
		mustCondition(t, "world.alpha", TruthTrue),
		mustCondition(t, "world.beta", TruthFalse),
	)
	data, err := jsonv2.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	var restored WorldState
	if err := jsonv2.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}
	if restored.Key() != state.Key() {
		t.Fatalf("restored key = %q, want %q", restored.Key(), state.Key())
	}
	for _, invalid := range []string{`null`, `{}`, `{"conditions":null}`, `{"conditions":[],"extra":true}`} {
		if err := jsonv2.Unmarshal([]byte(invalid), &restored); !errors.Is(err, ErrInvalidWorldState) {
			t.Fatalf("invalid WorldState %s error = %v", invalid, err)
		}
		if restored.Key() != state.Key() {
			t.Fatal("invalid WorldState replaced the last complete observation")
		}
	}
	var truth Truth
	if err := truth.UnmarshalJSON([]byte(`"true" false`)); !errors.Is(err, ErrInvalidCondition) {
		t.Fatalf("trailing Truth error = %v", err)
	}
}

func TestGoalAndActionOwnConstructionInputs(t *testing.T) {
	required := []Condition{mustCondition(t, "world.done", TruthTrue)}
	goal, err := NewGoal(GoalConfig{
		Name: "goal.done", Description: "Make the world done.", Conditions: required,
	})
	if err != nil {
		t.Fatal(err)
	}
	required[0] = mustCondition(t, "world.changed", TruthTrue)
	if goal.Conditions()[0].Key() != "world.done" {
		t.Fatal("Goal retained caller slice")
	}

	preconditions := []Condition{mustCondition(t, "world.ready", TruthTrue)}
	effects := []Condition{mustCondition(t, "world.done", TruthTrue)}
	action := mustAction(t, ActionConfig{
		Name: "action.finish", Description: "Finish the work.",
		Preconditions: preconditions, Effects: effects,
	})
	preconditions[0] = mustCondition(t, "world.changed", TruthTrue)
	effects[0] = mustCondition(t, "world.changed", TruthFalse)
	if action.Preconditions()[0].Key() != "world.ready" || action.Effects()[0].Key() != "world.done" {
		t.Fatal("Action retained caller slices")
	}
	cost, err := action.Cost(mustWorldState(t, mustCondition(t, "world.ready", TruthTrue)))
	if err != nil || cost != 1 {
		t.Fatalf("default cost = %v, error = %v", cost, err)
	}
}

func TestActionRejectsPredictiveNoOpAndInvalidCosts(t *testing.T) {
	ready := mustCondition(t, "world.ready", TruthTrue)
	_, err := NewAction(ActionConfig{
		Name: "action.noop", Description: "Predict no state change.",
		Preconditions: []Condition{ready}, Effects: []Condition{ready},
	})
	if !errors.Is(err, ErrInvalidAction) {
		t.Fatalf("no-op error = %v", err)
	}

	cause := errors.New("cost sentinel")
	tests := []struct {
		name string
		cost CostFunc
		want error
	}{
		{name: "negative", cost: FixedCost(-1), want: ErrInvalidActionCost},
		{name: "nan", cost: FixedCost(math.NaN()), want: ErrInvalidActionCost},
		{name: "infinite", cost: FixedCost(math.Inf(1)), want: ErrInvalidActionCost},
		{name: "error", cost: func(WorldState) (float64, error) { return 0, cause }, want: cause},
		{name: "panic", cost: func(WorldState) (float64, error) { panic(cause) }, want: cause},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			action := mustAction(t, ActionConfig{
				Name: "action.cost", Description: "Evaluate one test cost.",
				Effects: []Condition{mustCondition(t, "world.done", TruthTrue)}, Cost: test.cost,
			})
			_, err := action.Cost(WorldState{})
			if !errors.Is(err, ErrInvalidActionCost) || !errors.Is(err, test.want) {
				t.Fatalf("Cost error = %v", err)
			}
		})
	}
}

func TestProblemValidatesPlannerOutputAgainstItsActions(t *testing.T) {
	ready := mustCondition(t, "world.ready", TruthTrue)
	done := mustCondition(t, "world.done", TruthTrue)
	action := mustAction(t, ActionConfig{
		Name: "action.finish", Description: "Finish ready work.",
		Preconditions: []Condition{ready}, Effects: []Condition{done}, Cost: FixedCost(2),
	})
	goal := mustGoal(t, done)
	problem, err := NewProblem(mustWorldState(t, ready), goal, action)
	if err != nil {
		t.Fatal(err)
	}
	planned, err := NewPlannedAction("action.finish")
	if err != nil {
		t.Fatal(err)
	}
	valid, err := NewPlan([]PlannedAction{planned})
	if err != nil {
		t.Fatal(err)
	}
	if cost, evaluationErr := problem.EvaluatePlan(t.Context(), valid); evaluationErr != nil || cost != 2 {
		t.Fatalf("evaluated cost = %v, error = %v", cost, evaluationErr)
	}
	if data := mustJSON(t, valid); string(data) != `{"actions":["action.finish"]}` {
		t.Fatalf("Plan repeats predictive metadata: %s", data)
	}
	var retired Plan
	if decodeErr := jsonv2.Unmarshal([]byte(`{"actions":["action.finish"],"total_cost":2}`), &retired); !errors.Is(decodeErr, ErrInvalidPlan) {
		t.Fatalf("retired cost owner accepted: %v", decodeErr)
	}
	unknown, _ := NewPlannedAction("action.unknown")
	unknownPlan, _ := NewPlan([]PlannedAction{unknown})
	if _, err := problem.EvaluatePlan(t.Context(), unknownPlan); !errors.Is(err, ErrInvalidPlan) {
		t.Fatalf("unknown-Action error = %v", err)
	}
}

func TestOutputValidatesCompletedPlanningFacts(t *testing.T) {
	done := mustCondition(t, "world.done", TruthTrue)
	succeeded := Attempt{ActionName: "action.finish", Status: AttemptSucceeded}
	failed := Attempt{ActionName: "action.finish", Status: AttemptFailed, Diagnostic: "refused"}
	unconfirmed := Attempt{ActionName: "action.finish", Status: AttemptUnconfirmed}
	for _, test := range []struct {
		name     string
		outcome  Outcome
		attempts []Attempt
		passes   uint64
		valid    bool
	}{
		{name: "already achieved", outcome: OutcomeAchieved, valid: true},
		{name: "achieved after action", outcome: OutcomeAchieved, attempts: []Attempt{succeeded}, passes: 1, valid: true},
		{name: "unreachable", outcome: OutcomeUnreachable, passes: 1, valid: true},
		{name: "unreachable after attempt", outcome: OutcomeUnreachable, attempts: []Attempt{failed}},
		{name: "attempt limit exhausted", outcome: OutcomeExhausted, attempts: []Attempt{failed}, passes: 1, valid: true},
		{name: "exhausted without attempts", outcome: OutcomeExhausted},
		{name: "replanning stuck", outcome: OutcomeStuck, attempts: []Attempt{failed}, passes: 2, valid: true},
		{name: "stuck without attempts", outcome: OutcomeStuck},
		{name: "repeated success", outcome: OutcomeAchieved, attempts: []Attempt{succeeded, succeeded}, passes: 2, valid: true},
		{name: "attempt after failure", outcome: OutcomeAchieved, attempts: []Attempt{failed, succeeded}, passes: 2, valid: true},
		{name: "attempt after unconfirmed action", outcome: OutcomeStuck, attempts: []Attempt{unconfirmed, succeeded}, passes: 3, valid: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			output := Output{Outcome: test.outcome, Attempts: test.attempts}
			if test.outcome == OutcomeAchieved {
				output.WorldState = mustWorldState(t, done)
			}
			if err := output.Validate(); (err == nil) != test.valid || !test.valid && !errors.Is(err, ErrInvalidResult) {
				t.Fatalf("Validate = %v, want valid=%t", err, test.valid)
			}
			if test.valid && output.PlanningPasses() != test.passes {
				t.Fatalf("PlanningPasses = %d, want %d", output.PlanningPasses(), test.passes)
			}
		})
	}
}

func mustCondition(t *testing.T, key string, truth Truth) Condition {
	t.Helper()
	condition, err := NewCondition(key, truth)
	if err != nil {
		t.Fatal(err)
	}
	return condition
}

func mustWorldState(t *testing.T, conditions ...Condition) WorldState {
	t.Helper()
	state, err := NewWorldState(conditions...)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func mustGoal(t *testing.T, conditions ...Condition) Goal {
	t.Helper()
	goal, err := NewGoal(GoalConfig{
		Name: "goal.test", Description: "Reach the test target.", Conditions: conditions,
	})
	if err != nil {
		t.Fatal(err)
	}
	return goal
}

func mustAction(t *testing.T, config ActionConfig) Action {
	t.Helper()
	action, err := NewAction(config)
	if err != nil {
		t.Fatal(err)
	}
	return action
}

func TestAttemptAndOutputValidationShareResultClassification(t *testing.T) {
	for _, attempt := range []Attempt{
		{},
		{ActionName: "action", Status: AttemptSucceeded, Diagnostic: "unexpected"},
		{ActionName: "action", Status: AttemptFailed},
	} {
		if err := attempt.Validate(); !errors.Is(err, ErrInvalidResult) {
			t.Fatalf("attempt classification lost: %v", err)
		}
		if err := (Output{Outcome: OutcomeAchieved, Attempts: []Attempt{attempt}}).Validate(); !errors.Is(err, ErrInvalidResult) {
			t.Fatalf("nested attempt classification lost: %v", err)
		}
	}
	if err := (Output{}).Validate(); !errors.Is(err, ErrInvalidResult) {
		t.Fatalf("output classification lost: %v", err)
	}
}

func TestEvaluatePlanStopsAfterCanceledCost(t *testing.T) {
	for _, count := range []int{1, 2} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			calls := 0
			done := mustCondition(t, "world.done", TruthTrue)
			action := mustAction(t, ActionConfig{Name: "action.finish", Description: "Finish work.", Effects: []Condition{done}, Cost: func(WorldState) (float64, error) {
				calls++
				cancel()
				return 1, nil
			}})
			problem, err := NewProblem(WorldState{}, mustGoal(t, done), action)
			if err != nil {
				t.Fatal(err)
			}
			planned, _ := NewPlannedAction("action.finish")
			actions := make([]PlannedAction, count)
			for i := range actions {
				actions[i] = planned
			}
			plan, err := NewPlan(actions)
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

func TestProblemRejectsInapplicableIncompleteAndOverflowingPlans(t *testing.T) {
	ready := mustCondition(t, "world.ready", TruthTrue)
	done := mustCondition(t, "world.done", TruthTrue)
	prepare := mustAction(t, ActionConfig{
		Name: "prepare", Description: "Prepare work.", Effects: []Condition{ready}, Cost: FixedCost(math.MaxFloat64),
	})
	finish := mustAction(t, ActionConfig{
		Name: "finish", Description: "Finish ready work.", Preconditions: []Condition{ready}, Effects: []Condition{done}, Cost: FixedCost(math.MaxFloat64),
	})
	problem, err := NewProblem(WorldState{}, mustGoal(t, done), prepare, finish)
	if err != nil {
		t.Fatal(err)
	}
	for _, names := range [][]string{{"finish"}, {"prepare"}, {"prepare", "finish"}} {
		actions := make([]PlannedAction, len(names))
		for index, name := range names {
			actions[index], err = NewPlannedAction(name)
			if err != nil {
				t.Fatal(err)
			}
		}
		plan, err := NewPlan(actions)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := problem.EvaluatePlan(t.Context(), plan); !errors.Is(err, ErrInvalidPlan) {
			t.Fatalf("invalid sequence %v was accepted: %v", names, err)
		}
	}
	if _, err := (Problem{}).EvaluatePlan(t.Context(), Plan{}); !errors.Is(err, ErrInvalidPlan) {
		t.Fatalf("missing problem contract became a completed plan: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := problem.EvaluatePlan(ctx, Plan{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled validation became a planning result: %v", err)
	}
}

func TestPlanDecodingCannotReplaceAValidActionSequenceWithInvalidReferences(t *testing.T) {
	planned, err := NewPlannedAction("finish")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := NewPlan([]PlannedAction{planned})
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		`{"actions":[""]}`,
		`{"actions":["invalid name"]}`,
		`{"actions":[1]}`,
	} {
		if err := jsonv2.Unmarshal([]byte(raw), &plan); !errors.Is(err, ErrInvalidPlan) {
			t.Fatalf("invalid plan %s was accepted: %v", raw, err)
		}
		if actions := plan.Actions(); len(actions) != 1 || actions[0] != planned {
			t.Fatalf("failed decode replaced the last valid plan: %v", actions)
		}
	}
	if _, err := NewPlan([]PlannedAction{{}}); !errors.Is(err, ErrInvalidPlan) {
		t.Fatalf("invalid planned action was admitted: %v", err)
	}
	if _, err := jsonv2.Marshal(PlannedAction{}); !errors.Is(err, ErrInvalidPlan) {
		t.Fatalf("invalid planned action became portable: %v", err)
	}
}
