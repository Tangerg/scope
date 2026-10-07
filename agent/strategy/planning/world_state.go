package planning

import (
	jsonv2 "encoding/json/v2"
	"fmt"
	"slices"
	"strings"

	"github.com/Tangerg/scope/agent/internal/jsonwire"
)

// WorldState is an immutable, canonical observation of known condition truths.
// Missing conditions read as TruthUnknown. Its zero value is the empty state. Every
// value is valid: constructors and decoding establish invariants, and
// observations never expose mutable storage.
// Its JSON representation requires an explicit conditions array, even when empty.
type WorldState struct {
	conditions []Condition
}

func NewWorldState(conditions ...Condition) (WorldState, error) {
	values, err := canonicalConditions(conditions)
	if err != nil {
		return WorldState{}, fmt.Errorf("%w: %w", ErrInvalidWorldState, err)
	}
	return WorldState{conditions: values}, nil
}

// Conditions returns an independently owned, key-sorted snapshot.
func (w WorldState) Conditions() []Condition { return slices.Clone(w.conditions) }

func (w WorldState) Truth(key string) Truth {
	index, found := slices.BinarySearchFunc(w.conditions, key, func(condition Condition, key string) int {
		return strings.Compare(condition.key, key)
	})
	if !found {
		return TruthUnknown
	}
	return w.conditions[index].truth
}

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
	var size int
	for _, condition := range w.conditions {
		size += len(condition.key) + 3
	}
	var key strings.Builder
	key.Grow(size)
	for _, condition := range w.conditions {
		key.WriteString(condition.key)
		key.WriteByte('=')
		if condition.truth == TruthTrue {
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
	return jsonv2.Marshal(worldStateWire{Conditions: conditions})
}

func (w *WorldState) UnmarshalJSON(data []byte) error {
	if w == nil {
		return fmt.Errorf("%w: nil receiver", ErrInvalidWorldState)
	}
	wire, err := jsonwire.Decode[worldStateWire](data)
	if err != nil {
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
	Conditions []Condition `json:"conditions" jsonwire:"required"`
}

func (WorldState) JSONSchemaAlias() any { return worldStateWire{} }
