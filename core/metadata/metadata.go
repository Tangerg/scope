package metadata

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
)

var (
	ErrNilMap       = errors.New("metadata: nil map")
	ErrEmptyKey     = errors.New("metadata: empty key")
	ErrInvalidValue = errors.New("metadata: invalid JSON value")
)

// Map is the JSON-only extension boundary shared by protocol values. Keeping
// values encoded prevents runtime objects such as functions, readers, and SDK
// clients from entering DTOs unnoticed. Its zero value is writable through
// Set; Clone and Merge copy encoded bytes, and Merge validates both sides before
// changing the receiver. Equal intentionally compares the encoded form rather
// than performing semantic JSON normalization.
type Map map[string]json.RawMessage

// FromValues is the boundary where untyped decoded JSON becomes a validated
// Map. It exists so provider extensions and stored metadata are checked once
// on entry rather than trusted and then found unmarshalable at the next hop.
func FromValues(values map[string]any) (Map, error) {
	if values == nil {
		return nil, nil
	}
	encoded := make(Map, len(values))
	for key, value := range values {
		if err := encoded.Set(key, value); err != nil {
			return nil, err
		}
	}
	return encoded, nil
}

// Values returns an independently owned JSON value tree. Every number remains
// a json.Number, including nested values; consumers choose numeric conversions
// at their typed or provider boundary.
func (m Map) Values() (map[string]any, error) {
	if m == nil {
		return nil, nil
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	values := make(map[string]any, len(m))
	for key, raw := range m {
		value, err := decodeValue(raw)
		if err != nil {
			return nil, fmt.Errorf("metadata: decode %q: %w", key, err)
		}
		values[key] = value
	}
	return values, nil
}

func decodeValue(raw json.RawMessage) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	return value, nil
}

func (m *Map) Set(key string, value any) error {
	if m == nil {
		return ErrNilMap
	}
	if key == "" {
		return ErrEmptyKey
	}

	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("metadata: encode %q: %w", key, err)
	}
	if *m == nil {
		*m = make(Map, 1)
	}
	(*m)[key] = encoded
	return nil
}

func (m *Map) Merge(source Map) error {
	if m == nil {
		return ErrNilMap
	}
	if err := m.Validate(); err != nil {
		return fmt.Errorf("metadata: merge target: %w", err)
	}
	if err := source.Validate(); err != nil {
		return fmt.Errorf("metadata: merge source: %w", err)
	}
	if len(source) == 0 {
		return nil
	}
	if *m == nil {
		*m = make(Map, len(source))
	}
	for key, value := range source {
		(*m)[key] = bytes.Clone(value)
	}
	return nil
}

func (m Map) Decode[T any](key string) (T, bool, error) {
	var zero T
	if key == "" {
		return zero, false, ErrEmptyKey
	}
	raw, ok := m[key]
	if !ok {
		return zero, false, nil
	}
	if !json.Valid(raw) {
		return zero, true, fmt.Errorf("metadata: decode %q: %w", key, ErrInvalidValue)
	}

	var value T
	if err := json.Unmarshal(raw, &value); err != nil {
		return zero, true, fmt.Errorf("metadata: decode %q: %w", key, err)
	}
	return value, true, nil
}

func (m Map) IsZero() bool { return len(m) == 0 }

func (m Map) Clone() Map {
	if m == nil {
		return nil
	}
	clone := make(Map, len(m))
	for key, value := range m {
		clone[key] = bytes.Clone(value)
	}
	return clone
}

func (m Map) Equal(other Map) bool {
	return maps.EqualFunc(m, other, func(left, right json.RawMessage) bool {
		return bytes.Equal(left, right)
	})
}

func (m Map) Validate() error {
	for key, value := range m {
		if key == "" {
			return ErrEmptyKey
		}
		if !json.Valid(value) {
			return fmt.Errorf("metadata: key %q: %w", key, ErrInvalidValue)
		}
	}
	return nil
}

func (m Map) MarshalJSON() ([]byte, error) {
	if err := m.Validate(); err != nil {
		return nil, err
	}
	type wireMap Map
	return json.Marshal(wireMap(m))
}

func (m *Map) UnmarshalJSON(data []byte) error {
	if m == nil {
		return errors.New("metadata: map receiver is nil")
	}

	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(data, &decoded); err != nil {
		return fmt.Errorf("metadata: decode map: %w", err)
	}
	candidate := Map(decoded)
	if err := candidate.Validate(); err != nil {
		return err
	}
	*m = candidate
	return nil
}
