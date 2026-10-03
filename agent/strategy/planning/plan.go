package planning

import (
	jsonv2 "encoding/json/v2"
	"fmt"
	"slices"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/internal/jsonwire"
)

// PlannedAction is one immutable Action reference in Planner-selected order.
// It contains no executable capability or copied Action metadata.
type PlannedAction struct {
	name string
}

func NewPlannedAction(name string) (PlannedAction, error) {
	if !agent.ValidQualifiedName(name) {
		return PlannedAction{}, fmt.Errorf("%w: invalid Action name %q", ErrInvalidPlan, name)
	}
	return PlannedAction{name: name}, nil
}

func (p PlannedAction) Name() string { return p.name }

func (p PlannedAction) Valid() bool { return agent.ValidQualifiedName(p.name) }

func (p PlannedAction) MarshalJSON() ([]byte, error) {
	if !p.Valid() {
		return nil, ErrInvalidPlan
	}
	return jsonv2.Marshal(p.name)
}

func (p *PlannedAction) UnmarshalJSON(data []byte) error {
	if p == nil {
		return fmt.Errorf("%w: nil PlannedAction receiver", ErrInvalidPlan)
	}
	name, err := jsonwire.Decode[string](data)
	if err != nil {
		return fmt.Errorf("%w: decode PlannedAction: %w", ErrInvalidPlan, err)
	}
	value, err := NewPlannedAction(name)
	if err != nil {
		return err
	}
	*p = value
	return nil
}

// Plan is an immutable ordered Action sequence. Cost belongs to the Actions
// evaluated by Problem, never to the Planner's answer.
// An empty Plan is valid and represents an already-satisfied
// Goal; Planner's separate found result distinguishes it from no solution.
type Plan struct {
	actions []PlannedAction
}

func NewPlan(actions []PlannedAction) (Plan, error) {
	values := slices.Clone(actions)
	for index, action := range values {
		if !action.Valid() {
			return Plan{}, fmt.Errorf("%w: Action %d", ErrInvalidPlan, index)
		}
	}
	return Plan{actions: values}, nil
}

// Actions returns independently owned Action references in execution order.
func (p Plan) Actions() []PlannedAction { return slices.Clone(p.actions) }

func (p Plan) Valid() bool {
	for _, action := range p.actions {
		if !action.Valid() {
			return false
		}
	}
	return true
}

func (p Plan) MarshalJSON() ([]byte, error) {
	if !p.Valid() {
		return nil, ErrInvalidPlan
	}
	actions := slices.Clone(p.actions)
	if actions == nil {
		actions = []PlannedAction{}
	}
	return jsonv2.Marshal(planWire{Actions: actions})
}

func (p *Plan) UnmarshalJSON(data []byte) error {
	if p == nil {
		return fmt.Errorf("%w: nil receiver", ErrInvalidPlan)
	}
	wire, err := jsonwire.Decode[planWire](data, "actions")
	if err != nil {
		return fmt.Errorf("%w: decode: %w", ErrInvalidPlan, err)
	}
	value, err := NewPlan(wire.Actions)
	if err != nil {
		return err
	}
	*p = value
	return nil
}

type planWire struct {
	Actions []PlannedAction `json:"actions"`
}
