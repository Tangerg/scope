package planning

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"fmt"
	"math"
	"slices"
)

// PlannedAction is one immutable Action reference in Planner-selected order.
// It contains no executable capability or copied Action metadata.
type PlannedAction struct {
	name string
}

// NewPlannedAction references an Action by name so the Plan remains portable.
// The Definition supplies the authoritative Action behavior and metadata.
func NewPlannedAction(name string) (PlannedAction, error) {
	if !validName(name) {
		return PlannedAction{}, fmt.Errorf("%w: invalid Action name %q", ErrInvalidPlan, name)
	}
	return PlannedAction{name: name}, nil
}

// Name returns the referenced Action identity.
func (p PlannedAction) Name() string { return p.name }

func (p PlannedAction) Valid() bool { return validName(p.name) }

func (p PlannedAction) MarshalJSON() ([]byte, error) {
	if !p.Valid() {
		return nil, ErrInvalidPlan
	}
	return json.Marshal(p.name)
}

func (p *PlannedAction) UnmarshalJSON(data []byte) error {
	if p == nil {
		return fmt.Errorf("%w: nil PlannedAction receiver", ErrInvalidPlan)
	}
	var name string
	if err := jsonv2.Unmarshal(data, &name, jsonv2.RejectUnknownMembers(true)); err != nil {
		return fmt.Errorf("%w: decode PlannedAction: %w", ErrInvalidPlan, err)
	}
	value, err := NewPlannedAction(name)
	if err != nil {
		return err
	}
	*p = value
	return nil
}

// Plan is an immutable ordered Action sequence and its predicted total cost.
// An empty Plan with zero cost is valid and represents an already-satisfied
// Goal; Planner's separate found result distinguishes it from no solution.
type Plan struct {
	actions   []PlannedAction
	totalCost float64
}

// NewPlan carries the total cost alongside the steps so an operator can compare
// two plans without re-running the search that produced them.
func NewPlan(actions []PlannedAction, totalCost float64) (Plan, error) {
	if math.IsNaN(totalCost) || math.IsInf(totalCost, 0) || totalCost < 0 {
		return Plan{}, fmt.Errorf("%w: invalid total cost %v", ErrInvalidPlan, totalCost)
	}
	values := slices.Clone(actions)
	for index, action := range values {
		if !action.Valid() {
			return Plan{}, fmt.Errorf("%w: Action %d", ErrInvalidPlan, index)
		}
	}
	if len(values) == 0 && totalCost != 0 {
		return Plan{}, fmt.Errorf("%w: empty Plan must have zero cost", ErrInvalidPlan)
	}
	return Plan{actions: values, totalCost: totalCost}, nil
}

// Actions returns independently owned Action references in execution order.
func (p Plan) Actions() []PlannedAction { return slices.Clone(p.actions) }

// TotalCost returns the predicted sum of Action edge costs.
func (p Plan) TotalCost() float64 { return p.totalCost }

func (p Plan) Valid() bool {
	if math.IsNaN(p.totalCost) || math.IsInf(p.totalCost, 0) || p.totalCost < 0 ||
		len(p.actions) == 0 && p.totalCost != 0 {
		return false
	}
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
	return json.Marshal(planWire{Actions: actions, TotalCost: p.totalCost})
}

func (p *Plan) UnmarshalJSON(data []byte) error {
	if p == nil {
		return fmt.Errorf("%w: nil receiver", ErrInvalidPlan)
	}
	var wire planWire
	if err := jsonv2.Unmarshal(data, &wire, jsonv2.RejectUnknownMembers(true)); err != nil {
		return fmt.Errorf("%w: decode: %w", ErrInvalidPlan, err)
	}
	value, err := NewPlan(wire.Actions, wire.TotalCost)
	if err != nil {
		return err
	}
	*p = value
	return nil
}

type planWire struct {
	Actions   []PlannedAction `json:"actions"`
	TotalCost float64         `json:"total_cost"`
}
