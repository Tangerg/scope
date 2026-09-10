package planning_test

import (
	"errors"
	"testing"

	"github.com/Tangerg/scope/agent/strategy/planning"
)

func condition(t *testing.T, key string, truth planning.Truth) planning.Condition {
	t.Helper()
	value, err := planning.NewCondition(key, truth)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func worldState(t *testing.T, conditions ...planning.Condition) planning.WorldState {
	t.Helper()
	state, err := planning.NewWorldState(conditions...)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

// TestActionPublishesItsCompleteContract covers the accessors a planner and a
// human consumer read, plus the ownership rule behind them: a caller must not be
// able to reach back into an Action's condition sets, which the search treats as
// immutable.
func TestActionPublishesItsCompleteContract(t *testing.T) {
	action, err := planning.NewAction(planning.ActionConfig{
		Name:          "scope.test.gather",
		Description:   "gathers the required input",
		Preconditions: []planning.Condition{condition(t, "input.available", planning.True)},
		Effects:       []planning.Condition{condition(t, "input.gathered", planning.True)},
	})
	if err != nil {
		t.Fatal(err)
	}

	if action.Name() != "scope.test.gather" {
		t.Errorf("Name = %q", action.Name())
	}
	if action.Description() != "gathers the required input" {
		t.Errorf("Description = %q", action.Description())
	}

	preconditions := action.Preconditions()
	if len(preconditions) != 1 {
		t.Fatalf("Preconditions = %#v", preconditions)
	}
	preconditions[0] = planning.Condition{}
	if len(action.Preconditions()) != 1 || action.Preconditions()[0] == (planning.Condition{}) {
		t.Fatal("Preconditions aliases the Action's own set")
	}

	effects := action.Effects()
	effects[0] = planning.Condition{}
	if action.Effects()[0] == (planning.Condition{}) {
		t.Fatal("Effects aliases the Action's own set")
	}
}

// TestActionApplicabilityFollowsItsPreconditions is the property the search
// depends on: an Action is a legal edge exactly when the state establishes every
// precondition.
func TestActionApplicabilityFollowsItsPreconditions(t *testing.T) {
	action, err := planning.NewAction(planning.ActionConfig{
		Name:          "scope.test.gather",
		Description:   "gathers the required input",
		Preconditions: []planning.Condition{condition(t, "input.available", planning.True)},
		Effects:       []planning.Condition{condition(t, "input.gathered", planning.True)},
	})
	if err != nil {
		t.Fatal(err)
	}

	satisfied := worldState(t, condition(t, "input.available", planning.True))
	if !action.Applicable(satisfied) {
		t.Error("an Action was not applicable in a state that satisfies it")
	}

	unsatisfied := worldState(t, condition(t, "input.available", planning.False))
	if action.Applicable(unsatisfied) {
		t.Error("an Action was applicable in a state that contradicts it")
	}

	var zero planning.Action
	if zero.Applicable(satisfied) {
		t.Error("the zero Action reported itself applicable")
	}
}

// TestGoalPublishesItsCompleteContract mirrors the Action rules for the other
// half of the search: identity, human-readable intent, and an owned condition
// set.
func TestGoalPublishesItsCompleteContract(t *testing.T) {
	goal, err := planning.NewGoal(planning.GoalConfig{
		Name:        "scope.test.answered",
		Description: "the question is answered",
		Conditions:  []planning.Condition{condition(t, "answer.present", planning.True)},
	})
	if err != nil {
		t.Fatal(err)
	}

	if goal.Name() != "scope.test.answered" {
		t.Errorf("Name = %q", goal.Name())
	}
	if goal.Description() != "the question is answered" {
		t.Errorf("Description = %q", goal.Description())
	}

	conditions := goal.Conditions()
	conditions[0] = planning.Condition{}
	if goal.Conditions()[0] == (planning.Condition{}) {
		t.Fatal("Conditions aliases the goal's own set")
	}

	if !goal.SatisfiedBy(worldState(t, condition(t, "answer.present", planning.True))) {
		t.Error("a satisfying state did not satisfy the goal")
	}
	if goal.SatisfiedBy(worldState(t, condition(t, "answer.present", planning.False))) {
		t.Error("a contradicting state satisfied the goal")
	}
}

func TestNewGoalRequiresAtLeastOneCondition(t *testing.T) {
	_, err := planning.NewGoal(planning.GoalConfig{
		Name:        "scope.test.answered",
		Description: "the question is answered",
	})
	if !errors.Is(err, planning.ErrInvalidGoal) {
		t.Fatalf("NewGoal error = %v, want ErrInvalidGoal", err)
	}
}

// TestEmptyPlanMustHaveZeroCost is the invariant that keeps a plan's cost
// meaningful: a plan with no edges cannot have accumulated a cost.
func TestEmptyPlanMustHaveZeroCost(t *testing.T) {
	empty, err := planning.NewPlan(nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if empty.TotalCost() != 0 || len(empty.Actions()) != 0 {
		t.Fatalf("empty plan = %#v", empty)
	}
	if !empty.Valid() {
		t.Fatal("an empty zero-cost plan reported itself invalid")
	}

	if _, err := planning.NewPlan(nil, 1); !errors.Is(err, planning.ErrInvalidPlan) {
		t.Fatalf("NewPlan error = %v, want ErrInvalidPlan", err)
	}
}

func TestTruthIsAClosedVocabulary(t *testing.T) {
	for _, truth := range []planning.Truth{planning.True, planning.False} {
		if truth.String() == "" {
			t.Errorf("%v printed nothing", truth)
		}
	}
	if _, err := planning.NewCondition("key", planning.Truth("maybe")); err == nil {
		t.Fatal("NewCondition accepted a truth outside the vocabulary")
	}
	if _, err := planning.NewCondition("", planning.True); err == nil {
		t.Fatal("NewCondition accepted an empty key")
	}
}
