package planning

import (
	"fmt"
	"math"
	"slices"
	"strings"
)

// CostFunc lets an action's cost depend on the world state it would run in,
// which is what allows a planner to prefer a cheap path under current facts
// rather than a fixed ordering. Returning an error keeps an uncomputable cost
// from being silently treated as zero, which would make that action always
// win.
type CostFunc func(source WorldState) (float64, error)

// FixedCost returns a CostFunc that always returns value. Validation occurs
// when an Action evaluates the cost so the same error contract covers fixed and
// dynamic costs.
func FixedCost(value float64) CostFunc {
	return func(WorldState) (float64, error) { return value, nil }
}

// ActionConfig contains one Action's complete predictive planning semantics.
// External execution is deliberately absent and is bound separately by a
// managed Planning Definition.
type ActionConfig struct {
	// Name is the stable lower-case qualified Action identity.
	Name string

	// Description explains what the Action is expected to accomplish.
	Description string

	// Preconditions are truths required in the source WorldState.
	Preconditions []Condition

	// Effects are truths predicted in the successor WorldState after success.
	Effects []Condition

	// Cost computes the non-negative search edge cost. Nil defaults to 1.
	Cost CostFunc
}

// Action is an immutable predictive operation used only by Planners. It does
// not execute I/O and is not a model Tool.
type Action struct {
	name          string
	description   string
	preconditions []Condition
	effects       []Condition
	cost          CostFunc
}

// NewAction builds the predictive half of an action — preconditions, effects,
// and cost — with no executable body. Keeping execution out means the planner
// can search over actions without the risk of running one, and the same action
// can be bound to different executors.
func NewAction(config ActionConfig) (Action, error) {
	if !validName(config.Name) {
		return Action{}, fmt.Errorf("%w: invalid name %q", ErrInvalidAction, config.Name)
	}
	if !validDescription(config.Description) {
		return Action{}, fmt.Errorf("%w: Description must be non-empty, trimmed UTF-8 within %d bytes", ErrInvalidAction, maxDescriptionBytes)
	}
	preconditions, err := canonicalConditions(config.Preconditions)
	if err != nil {
		return Action{}, fmt.Errorf("%w: preconditions: %w", ErrInvalidAction, err)
	}
	effects, err := canonicalConditions(config.Effects)
	if err != nil {
		return Action{}, fmt.Errorf("%w: effects: %w", ErrInvalidAction, err)
	}
	if len(effects) == 0 {
		return Action{}, fmt.Errorf("%w: at least one effect is required", ErrInvalidAction)
	}
	if !changesAnyCondition(preconditions, effects) {
		return Action{}, fmt.Errorf("%w: effects cannot change any state satisfying the preconditions", ErrInvalidAction)
	}
	cost := config.Cost
	if cost == nil {
		cost = FixedCost(1)
	}
	return Action{
		name: config.Name, description: config.Description,
		preconditions: preconditions, effects: effects, cost: cost,
	}, nil
}

// Name returns the stable Action identity.
func (a Action) Name() string { return a.name }

// Description returns the human-readable predicted behavior.
func (a Action) Description() string { return a.description }

// Preconditions returns an independently owned, key-sorted requirement set.
func (a Action) Preconditions() []Condition { return slices.Clone(a.preconditions) }

// Effects returns an independently owned, key-sorted prediction set.
func (a Action) Effects() []Condition { return slices.Clone(a.effects) }

// Applicable reports whether state establishes every Action precondition.
func (a Action) Applicable(state WorldState) bool {
	return a.Valid() && state.Valid() && state.Satisfies(a.preconditions...)
}

// Cost evaluates the Action's predicted edge cost against source. Panics,
// errors, negative values, and non-finite values are returned as
// ErrInvalidActionCost.
func (a Action) Cost(source WorldState) (cost float64, err error) {
	if !a.Valid() || !source.Valid() {
		return 0, fmt.Errorf("%w: invalid Action or source WorldState", ErrInvalidActionCost)
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			cost = 0
			if cause, ok := recovered.(error); ok {
				err = fmt.Errorf("%w: Action %q panicked: %w", ErrInvalidActionCost, a.name, cause)
				return
			}
			err = fmt.Errorf("%w: Action %q panicked: %v", ErrInvalidActionCost, a.name, recovered)
		}
	}()
	cost, err = a.cost(source)
	if err != nil {
		return 0, fmt.Errorf("%w: Action %q: %w", ErrInvalidActionCost, a.name, err)
	}
	if math.IsNaN(cost) || math.IsInf(cost, 0) || cost < 0 {
		return 0, fmt.Errorf("%w: Action %q returned %v", ErrInvalidActionCost, a.name, cost)
	}
	return cost, nil
}

// Apply returns the Action's predicted successor state. It does not assert that
// external execution actually produced the prediction.
func (a Action) Apply(source WorldState) (WorldState, error) {
	if !a.Valid() || !source.Valid() {
		return WorldState{}, ErrInvalidAction
	}
	return source.Apply(a.effects...)
}

func (a Action) Valid() bool {
	return validName(a.name) && validDescription(a.description) &&
		canonicalConditionSlice(a.preconditions) && len(a.effects) > 0 &&
		canonicalConditionSlice(a.effects) && a.cost != nil &&
		changesAnyCondition(a.preconditions, a.effects)
}

func changesAnyCondition(preconditions, effects []Condition) bool {
	for _, effect := range effects {
		index, found := slices.BinarySearchFunc(preconditions, effect.key, func(condition Condition, key string) int {
			return strings.Compare(condition.key, key)
		})
		if !found || preconditions[index].truth != effect.truth {
			return true
		}
	}
	return false
}

func canonicalConditions(conditions []Condition) ([]Condition, error) {
	values := slices.Clone(conditions)
	for index, condition := range values {
		if !condition.Valid() {
			return nil, fmt.Errorf("condition %d: %w", index, ErrInvalidCondition)
		}
	}
	slices.SortFunc(values, func(left, right Condition) int {
		return strings.Compare(left.key, right.key)
	})
	for index := 1; index < len(values); index++ {
		if values[index-1].key == values[index].key {
			return nil, fmt.Errorf("duplicate condition %q", values[index].key)
		}
	}
	return values, nil
}

func canonicalConditionSlice(conditions []Condition) bool {
	for index, condition := range conditions {
		if !condition.Valid() || index > 0 && conditions[index-1].key >= condition.key {
			return false
		}
	}
	return true
}
