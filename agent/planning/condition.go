package planning

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"fmt"
)

// Condition is one immutable known truth requirement or prediction. Unknown is
// represented by absence from a WorldState and therefore cannot be stored in a
// Condition.
type Condition struct {
	key   string
	truth Truth
}

// NewCondition names one fact the planner reasons over. Conditions are keys
// with a truth value rather than arbitrary predicates, because the planner has
// to compare and combine them without executing anything.
func NewCondition(key string, truth Truth) (Condition, error) {
	condition := Condition{key: key, truth: truth}
	if !condition.Valid() {
		return Condition{}, fmt.Errorf("%w: key %q and truth %s", ErrInvalidCondition, key, truth)
	}
	return condition, nil
}

// Key returns the stable condition identity.
func (c Condition) Key() string { return c.key }

// Truth returns the known truth asserted by the condition.
func (c Condition) Truth() Truth { return c.truth }

func (c Condition) Valid() bool {
	return validName(c.key) && c.truth.known()
}

func (c Condition) MarshalJSON() ([]byte, error) {
	if !c.Valid() {
		return nil, ErrInvalidCondition
	}
	return json.Marshal(conditionWire{Key: c.key, Truth: c.truth})
}

func (c *Condition) UnmarshalJSON(data []byte) error {
	if c == nil {
		return fmt.Errorf("%w: nil receiver", ErrInvalidCondition)
	}
	var wire conditionWire
	if err := jsonv2.Unmarshal(data, &wire, jsonv2.RejectUnknownMembers(true)); err != nil {
		return fmt.Errorf("%w: decode: %w", ErrInvalidCondition, err)
	}
	value, err := NewCondition(wire.Key, wire.Truth)
	if err != nil {
		return err
	}
	*c = value
	return nil
}

type conditionWire struct {
	Key   string `json:"key" jsonschema:"pattern=^[a-z][a-z0-9._-]{0\\,127}$"`
	Truth Truth  `json:"truth" jsonschema:"enum=false,enum=true"`
}

// JSONSchemaAlias returns the typed JSON wire model owned by Condition.
func (Condition) JSONSchemaAlias() any { return conditionWire{} }
