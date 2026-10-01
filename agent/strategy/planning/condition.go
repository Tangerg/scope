package planning

import (
	jsonv2 "encoding/json/v2"
	"fmt"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/internal/jsonwire"
)

// Condition is one immutable known truth requirement or prediction.
// TruthUnknown is represented by absence from a WorldState and therefore cannot be stored in a
// Condition.
type Condition struct {
	key   string
	truth Truth
}

func NewCondition(key string, truth Truth) (Condition, error) {
	condition := Condition{key: key, truth: truth}
	if !agent.ValidQualifiedName(key) || !truth.known() {
		return Condition{}, fmt.Errorf("%w: key %q and truth %s", ErrInvalidCondition, key, truth)
	}
	return condition, nil
}

func (c Condition) Key() string { return c.key }

func (c Condition) Truth() Truth { return c.truth }

func (c Condition) Valid() bool { return c.key != "" }

func (c Condition) MarshalJSON() ([]byte, error) {
	if !c.Valid() {
		return nil, ErrInvalidCondition
	}
	return jsonv2.Marshal(conditionWire{Key: c.key, Truth: c.truth})
}

func (c *Condition) UnmarshalJSON(data []byte) error {
	if c == nil {
		return fmt.Errorf("%w: nil receiver", ErrInvalidCondition)
	}
	wire, err := jsonwire.Decode[conditionWire](data)
	if err != nil {
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

func (Condition) JSONSchemaAlias() any { return conditionWire{} }
