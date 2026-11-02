package planning

import (
	"errors"
	"testing"
)

func condition(t *testing.T, key string, truth Truth) Condition {
	t.Helper()
	value, err := NewCondition(key, truth)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func worldState(t *testing.T, conditions ...Condition) WorldState {
	t.Helper()
	state, err := NewWorldState(conditions...)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func TestActionPublishesItsCompleteContract(t *testing.T) {
	action, err := NewAction(ActionConfig{
		Name:          "scope.test.gather",
		Description:   "gathers the required input",
		Preconditions: []Condition{condition(t, "input.available", TruthTrue)},
		Effects:       []Condition{condition(t, "input.gathered", TruthTrue)},
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
	preconditions[0] = Condition{}
	if len(action.Preconditions()) != 1 || action.Preconditions()[0] == (Condition{}) {
		t.Fatal("Preconditions aliases the Action's own set")
	}

	effects := action.Effects()
	effects[0] = Condition{}
	if action.Effects()[0] == (Condition{}) {
		t.Fatal("Effects aliases the Action's own set")
	}
}

func TestActionApplicabilityFollowsItsPreconditions(t *testing.T) {
	action, err := NewAction(ActionConfig{
		Name:          "scope.test.gather",
		Description:   "gathers the required input",
		Preconditions: []Condition{condition(t, "input.available", TruthTrue)},
		Effects:       []Condition{condition(t, "input.gathered", TruthTrue)},
	})
	if err != nil {
		t.Fatal(err)
	}

	satisfied := worldState(t, condition(t, "input.available", TruthTrue))
	if !action.Applicable(satisfied) {
		t.Error("an Action was not applicable in a state that satisfies it")
	}

	unsatisfied := worldState(t, condition(t, "input.available", TruthFalse))
	if action.Applicable(unsatisfied) {
		t.Error("an Action was applicable in a state that contradicts it")
	}

	var zero Action
	if zero.Applicable(satisfied) {
		t.Error("the zero Action reported itself applicable")
	}
}

func TestGoalPublishesItsCompleteContract(t *testing.T) {
	goal, err := NewGoal(GoalConfig{
		Name:        "scope.test.answered",
		Description: "the question is answered",
		Conditions:  []Condition{condition(t, "answer.present", TruthTrue)},
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
	conditions[0] = Condition{}
	if goal.Conditions()[0] == (Condition{}) {
		t.Fatal("Conditions aliases the goal's own set")
	}

	if !goal.SatisfiedBy(worldState(t, condition(t, "answer.present", TruthTrue))) {
		t.Error("a satisfying state did not satisfy the goal")
	}
	if goal.SatisfiedBy(worldState(t, condition(t, "answer.present", TruthFalse))) {
		t.Error("a contradicting state satisfied the goal")
	}
}

func TestNewGoalRequiresAtLeastOneCondition(t *testing.T) {
	_, err := NewGoal(GoalConfig{
		Name:        "scope.test.answered",
		Description: "the question is answered",
	})
	if !errors.Is(err, ErrInvalidGoal) {
		t.Fatalf("NewGoal error = %v, want ErrInvalidGoal", err)
	}
}

func TestEmptyPlanRepresentsAnEmptyActionSequence(t *testing.T) {
	empty, err := NewPlan(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(empty.Actions()) != 0 {
		t.Fatalf("empty plan = %#v", empty)
	}
}

func TestTruthIsAClosedVocabulary(t *testing.T) {
	for _, truth := range []Truth{TruthTrue, TruthFalse} {
		if truth.String() == "" {
			t.Errorf("%v printed nothing", truth)
		}
	}
	if _, err := NewCondition("key", Truth("maybe")); err == nil {
		t.Fatal("NewCondition accepted a truth outside the vocabulary")
	}
	if _, err := NewCondition("", TruthTrue); err == nil {
		t.Fatal("NewCondition accepted an empty key")
	}
}
