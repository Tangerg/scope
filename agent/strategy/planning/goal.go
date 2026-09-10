package planning

import (
	"fmt"
	"slices"
)

// GoalConfig contains the complete immutable description of a Planning goal.
type GoalConfig struct {
	// Name is the stable lower-case qualified goal identity.
	Name string

	// Description explains the desired state to a human consumer.
	Description string

	// Conditions are the known truths the final WorldState must establish.
	Conditions []Condition
}

// Goal is an immutable set of desired condition truths.
type Goal struct {
	name        string
	description string
	conditions  []Condition
}

// NewGoal states the target as conditions rather than as a procedure, which is
// what lets the planner decide the route and re-plan when observed facts
// change.
func NewGoal(config GoalConfig) (Goal, error) {
	if !validName(config.Name) {
		return Goal{}, fmt.Errorf("%w: invalid name %q", ErrInvalidGoal, config.Name)
	}
	if !validDescription(config.Description) {
		return Goal{}, fmt.Errorf("%w: Description must be non-empty, trimmed UTF-8 within %d bytes", ErrInvalidGoal, maxDescriptionBytes)
	}
	conditions, err := canonicalConditions(config.Conditions)
	if err != nil {
		return Goal{}, fmt.Errorf("%w: conditions: %w", ErrInvalidGoal, err)
	}
	if len(conditions) == 0 {
		return Goal{}, fmt.Errorf("%w: at least one condition is required", ErrInvalidGoal)
	}
	return Goal{name: config.Name, description: config.Description, conditions: conditions}, nil
}

// Name returns the stable goal identity.
func (g Goal) Name() string { return g.name }

// Description returns the human-readable desired state.
func (g Goal) Description() string { return g.description }

// Conditions returns an independently owned, key-sorted requirement set.
func (g Goal) Conditions() []Condition { return slices.Clone(g.conditions) }

// SatisfiedBy reports whether state establishes every goal condition.
func (g Goal) SatisfiedBy(state WorldState) bool {
	return g.Valid() && state.Valid() && state.Satisfies(g.conditions...)
}

func (g Goal) Valid() bool {
	return validName(g.name) && validDescription(g.description) && len(g.conditions) > 0 &&
		canonicalConditionSlice(g.conditions)
}
