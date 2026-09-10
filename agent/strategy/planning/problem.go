package planning

import (
	"fmt"
	"math"
	"slices"
)

// Problem is one immutable Planner input: a current observation, one Goal, and
// the available predictive Actions. It contains no dispatcher, Process, or
// application dependency.
type Problem struct {
	initial WorldState
	goal    Goal
	actions []Action
}

// NewProblem binds the initial state, the goal, and the actions available to
// reach it into one value, so a planner cannot be handed a goal without the
// vocabulary it is expected to search over.
func NewProblem(initial WorldState, goal Goal, actions ...Action) (Problem, error) {
	if !initial.Valid() {
		return Problem{}, fmt.Errorf("%w: initial state", ErrInvalidProblem)
	}
	if !goal.Valid() {
		return Problem{}, fmt.Errorf("%w: Goal", ErrInvalidProblem)
	}
	values := slices.Clone(actions)
	seen := make(map[string]struct{}, len(values))
	for index, action := range values {
		if !action.Valid() {
			return Problem{}, fmt.Errorf("%w: Action %d", ErrInvalidProblem, index)
		}
		if _, duplicate := seen[action.name]; duplicate {
			return Problem{}, fmt.Errorf("%w: duplicate Action %q", ErrInvalidProblem, action.name)
		}
		seen[action.name] = struct{}{}
	}
	return Problem{initial: initial, goal: goal, actions: values}, nil
}

// InitialState returns the immutable starting observation.
func (p Problem) InitialState() WorldState { return p.initial }

// Goal returns the immutable desired state.
func (p Problem) Goal() Goal { return p.goal }

// Actions returns an independently owned slice in declaration order.
func (p Problem) Actions() []Action { return slices.Clone(p.actions) }

// Action returns the named Action and true, or the zero Action and false.
func (p Problem) Action(name string) (Action, bool) {
	for _, action := range p.actions {
		if action.name == name {
			return action, true
		}
	}
	return Action{}, false
}

func (p Problem) Valid() bool {
	if !p.initial.Valid() || !p.goal.Valid() {
		return false
	}
	seen := make(map[string]struct{}, len(p.actions))
	for _, action := range p.actions {
		if !action.Valid() {
			return false
		}
		if _, duplicate := seen[action.name]; duplicate {
			return false
		}
		seen[action.name] = struct{}{}
	}
	return true
}

// ValidatePlan verifies that every referenced Action exists and is applicable
// in sequence, the reported cost equals the evaluated path cost, and the
// predicted final state satisfies the Goal.
func (p Problem) ValidatePlan(plan Plan) error {
	if !p.Valid() || !plan.Valid() {
		return ErrInvalidPlan
	}
	state := p.initial
	totalCost := 0.0
	for index, planned := range plan.actions {
		action, found := p.Action(planned.name)
		if !found {
			return fmt.Errorf("%w: Action %d references unknown %q", ErrInvalidPlan, index, planned.name)
		}
		if !action.Applicable(state) {
			return fmt.Errorf("%w: Action %q is not applicable at step %d", ErrInvalidPlan, planned.name, index)
		}
		cost, err := action.Cost(state)
		if err != nil {
			return err
		}
		totalCost += cost
		if math.IsInf(totalCost, 0) {
			return fmt.Errorf("%w: total cost overflow", ErrInvalidPlan)
		}
		state, err = action.Apply(state)
		if err != nil {
			return fmt.Errorf("%w: apply Action %q: %w", ErrInvalidPlan, planned.name, err)
		}
	}
	if totalCost != plan.totalCost {
		return fmt.Errorf("%w: total cost %v does not equal evaluated cost %v", ErrInvalidPlan, plan.totalCost, totalCost)
	}
	if !p.goal.SatisfiedBy(state) {
		return fmt.Errorf("%w: predicted final state does not satisfy Goal %q", ErrInvalidPlan, p.goal.name)
	}
	return nil
}
