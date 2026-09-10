package planning

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"fmt"
	"slices"
	"strings"
)

// WorldState is an immutable, canonical observation of known condition truths.
// Missing conditions read as Unknown. Its zero value is the valid empty state.
type WorldState struct {
	conditions []Condition
}

// NewWorldState collects the facts a plan is evaluated against. It is a
// validated set rather than a free-form map because the planner compares
// states for equality while searching, and duplicate or conflicting conditions
// would make that comparison meaningless.
func NewWorldState(conditions ...Condition) (WorldState, error) {
	values := slices.Clone(conditions)
	for index, condition := range values {
		if !condition.Valid() {
			return WorldState{}, fmt.Errorf("%w: condition %d", ErrInvalidWorldState, index)
		}
	}
	slices.SortFunc(values, func(left, right Condition) int {
		return strings.Compare(left.key, right.key)
	})
	for index := 1; index < len(values); index++ {
		if values[index-1].key == values[index].key {
			return WorldState{}, fmt.Errorf("%w: duplicate condition %q", ErrInvalidWorldState, values[index].key)
		}
	}
	return WorldState{conditions: values}, nil
}

// Conditions returns an independently owned, key-sorted snapshot.
func (w WorldState) Conditions() []Condition { return slices.Clone(w.conditions) }

// Truth returns the observed truth for key, or Unknown when key is absent.
func (w WorldState) Truth(key string) Truth {
	index, found := slices.BinarySearchFunc(w.conditions, key, func(condition Condition, key string) int {
		return strings.Compare(condition.key, key)
	})
	if !found {
		return Unknown
	}
	return w.conditions[index].truth
}

// Satisfies reports whether w establishes every required condition.
func (w WorldState) Satisfies(requirements ...Condition) bool {
	for _, requirement := range requirements {
		if !requirement.Valid() || w.Truth(requirement.key) != requirement.truth {
			return false
		}
	}
	return true
}

// Apply returns a new state with predicted effects layered over this state.
// The receiver is never mutated.
func (w WorldState) Apply(effects ...Condition) (WorldState, error) {
	if !w.Valid() {
		return WorldState{}, ErrInvalidWorldState
	}
	values := make(map[string]Truth, len(w.conditions)+len(effects))
	for _, condition := range w.conditions {
		values[condition.key] = condition.truth
	}
	for index, effect := range effects {
		if !effect.Valid() {
			return WorldState{}, fmt.Errorf("%w: effect %d", ErrInvalidWorldState, index)
		}
		values[effect.key] = effect.truth
	}
	conditions := make([]Condition, 0, len(values))
	for key, truth := range values {
		conditions = append(conditions, Condition{key: key, truth: truth})
	}
	return NewWorldState(conditions...)
}

// Key returns a stable identity derived only from canonical known truths.
func (w WorldState) Key() string {
	var key strings.Builder
	for _, condition := range w.conditions {
		key.WriteString(condition.key)
		key.WriteByte('=')
		if condition.truth == True {
			key.WriteByte('1')
		} else {
			key.WriteByte('0')
		}
		key.WriteByte('|')
	}
	return key.String()
}

func (w WorldState) Valid() bool {
	for index, condition := range w.conditions {
		if !condition.Valid() || index > 0 && w.conditions[index-1].key >= condition.key {
			return false
		}
	}
	return true
}

func (w WorldState) MarshalJSON() ([]byte, error) {
	if !w.Valid() {
		return nil, ErrInvalidWorldState
	}
	conditions := slices.Clone(w.conditions)
	if conditions == nil {
		conditions = []Condition{}
	}
	return json.Marshal(worldStateWire{Conditions: conditions})
}

func (w *WorldState) UnmarshalJSON(data []byte) error {
	if w == nil {
		return fmt.Errorf("%w: nil receiver", ErrInvalidWorldState)
	}
	var wire worldStateWire
	if err := jsonv2.Unmarshal(data, &wire, jsonv2.RejectUnknownMembers(true)); err != nil {
		return fmt.Errorf("%w: decode: %w", ErrInvalidWorldState, err)
	}
	value, err := NewWorldState(wire.Conditions...)
	if err != nil {
		return err
	}
	*w = value
	return nil
}

type worldStateWire struct {
	Conditions []Condition `json:"conditions"`
}

// JSONSchemaAlias returns the typed JSON wire model owned by WorldState.
func (WorldState) JSONSchemaAlias() any { return worldStateWire{} }
