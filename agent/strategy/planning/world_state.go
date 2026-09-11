package planning

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"fmt"
	"slices"
	"strings"
)

// WorldState is an immutable, canonical observation of known condition truths.
// Missing conditions read as Unknown. Its zero value is the empty state. Every
// value is valid: constructors and decoding establish invariants, and
// observations never expose mutable storage.
type WorldState struct {
	conditions []Condition
}

// NewWorldState collects the facts a plan is evaluated against. It is a
// validated set rather than a free-form map because the planner compares
// states for equality while searching, and duplicate or conflicting conditions
// would make that comparison meaningless.
func NewWorldState(conditions ...Condition) (WorldState, error) {
	values, err := canonicalConditions(conditions)
	if err != nil {
		return WorldState{}, fmt.Errorf("%w: %w", ErrInvalidWorldState, err)
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
// The receiver is never mutated. The last effect for a repeated key wins.
func (w WorldState) Apply(effects ...Condition) (WorldState, error) {
	values := slices.Clone(effects)
	for index, effect := range values {
		if !effect.Valid() {
			return WorldState{}, fmt.Errorf("%w: effect %d", ErrInvalidWorldState, index)
		}
	}
	slices.SortStableFunc(values, func(left, right Condition) int {
		return strings.Compare(left.key, right.key)
	})
	canonical := values[:0]
	for _, effect := range values {
		if len(canonical) > 0 && canonical[len(canonical)-1].key == effect.key {
			canonical[len(canonical)-1] = effect
		} else {
			canonical = append(canonical, effect)
		}
	}
	return w.apply(canonical), nil
}

// Both inputs already own sorted, unique conditions. Preserve that invariant
// while constructing the successor instead of rebuilding an untrusted state.
func (w WorldState) apply(effects []Condition) WorldState {
	if len(effects) == 0 {
		return w
	}
	conditions := make([]Condition, 0, len(w.conditions)+len(effects))
	left, right := 0, 0
	for left < len(w.conditions) && right < len(effects) {
		switch strings.Compare(w.conditions[left].key, effects[right].key) {
		case -1:
			conditions = append(conditions, w.conditions[left])
			left++
		case 0:
			conditions = append(conditions, effects[right])
			left++
			right++
		case 1:
			conditions = append(conditions, effects[right])
			right++
		}
	}
	conditions = append(conditions, w.conditions[left:]...)
	conditions = append(conditions, effects[right:]...)
	return WorldState{conditions: conditions}
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

func (w WorldState) MarshalJSON() ([]byte, error) {
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
