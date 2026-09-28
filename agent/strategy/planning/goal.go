package planning

import (
	"fmt"
	"slices"

	agent "github.com/Tangerg/scope/agent"
)

type GoalConfig struct {
	Name string

	Description string

	Conditions []Condition
}

// Goal is an immutable set of desired condition truths.
type Goal struct {
	name        string
	description string
	conditions  []Condition
}

func NewGoal(config GoalConfig) (Goal, error) {
	if !agent.ValidQualifiedName(config.Name) {
		return Goal{}, fmt.Errorf("%w: invalid name %q", ErrInvalidGoal, config.Name)
	}
	if !agent.ValidDescription(config.Description) {
		return Goal{}, fmt.Errorf("%w: Description must be non-empty, trimmed UTF-8 within %d bytes", ErrInvalidGoal, agent.MaxDescriptionBytes)
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

func (g Goal) Name() string { return g.name }

func (g Goal) Description() string { return g.description }

// Conditions returns an independently owned, key-sorted requirement set.
func (g Goal) Conditions() []Condition { return slices.Clone(g.conditions) }

func (g Goal) SatisfiedBy(state WorldState) bool {
	return g.Valid() && state.Satisfies(g.conditions...)
}

func (g Goal) Valid() bool { return len(g.conditions) > 0 }
