package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
)

var ErrInvalidCapability = errors.New("agent: invalid capability")

// Capability is one stable qualified authority name understood by a
// Deployment's dispatcher or another external boundary. The Framework only
// enforces possession and attenuation; it does not assign product meaning.
type Capability struct{ name string }

// ParseCapability validates a lowercase qualified capability name.
func ParseCapability(name string) (Capability, error) {
	if !ValidQualifiedName(name) {
		return Capability{}, ErrInvalidCapability
	}
	return Capability{name: name}, nil
}

func (c Capability) String() string { return c.name }

func (c Capability) compare(other Capability) int { return strings.Compare(c.name, other.name) }

func (c Capability) Valid() bool { return ValidQualifiedName(c.name) }

func (c Capability) MarshalText() ([]byte, error) {
	if !c.Valid() {
		return nil, ErrInvalidCapability
	}
	return []byte(c.name), nil
}

func (Capability) JSONSchemaAlias() any { return "" }

func (c *Capability) UnmarshalText(text []byte) error {
	if c == nil {
		return ErrInvalidCapability
	}
	value, err := ParseCapability(string(text))
	if err != nil {
		return err
	}
	*c = value
	return nil
}

// CapabilitySet is an immutable, sorted set of authority names. Its zero value
// is the valid empty set, encoded as an empty JSON array. JSON null is invalid.
type CapabilitySet struct{ values []Capability }

// NewCapabilitySet builds the frozen grant a Process runs under. It is a set
// rather than a slice because a duplicated or reordered grant must not change
// authority, and because every child grant must be a subset of its parent's.
func NewCapabilitySet(capabilities ...Capability) (CapabilitySet, error) {
	values := slices.Clone(capabilities)
	for _, capability := range values {
		if !capability.Valid() {
			return CapabilitySet{}, ErrInvalidCapability
		}
	}
	slices.SortFunc(values, Capability.compare)
	values = slices.Compact(values)
	return CapabilitySet{values: values}, nil
}

// Values returns an independently owned, sorted capability slice.
func (c CapabilitySet) Values() []Capability { return slices.Clone(c.values) }

// Contains reports whether capability belongs to the set.
func (c CapabilitySet) Contains(capability Capability) bool {
	if !capability.Valid() {
		return false
	}
	_, found := slices.BinarySearchFunc(c.values, capability, Capability.compare)
	return found
}

// Allows reports whether requested is a subset of c.
func (c CapabilitySet) Allows(requested CapabilitySet) bool {
	if !c.Valid() || !requested.Valid() {
		return false
	}
	for _, capability := range requested.values {
		if !c.Contains(capability) {
			return false
		}
	}
	return true
}

func (c CapabilitySet) Valid() bool {
	for index, capability := range c.values {
		if !capability.Valid() || index > 0 && c.values[index-1].compare(capability) >= 0 {
			return false
		}
	}
	return true
}

func (c CapabilitySet) MarshalJSON() ([]byte, error) {
	if !c.Valid() {
		return nil, ErrInvalidCapability
	}
	if len(c.values) == 0 {
		return []byte("[]"), nil
	}
	return json.Marshal(c.values)
}

func (CapabilitySet) JSONSchemaAlias() any { return []Capability{} }

// UnmarshalJSON rejects duplicate grants and null; ordering is normalized.
// Construction accepts duplicates because its arguments describe a set.
func (c *CapabilitySet) UnmarshalJSON(data []byte) error {
	if c == nil {
		return ErrInvalidCapability
	}
	values, err := decodeJSON[[]Capability](data)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidCapability, err)
	}
	if values == nil {
		return ErrInvalidCapability
	}
	value, err := NewCapabilitySet(values...)
	if err != nil || len(value.values) != len(values) {
		return ErrInvalidCapability
	}
	*c = value
	return nil
}
