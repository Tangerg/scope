package metadata

import (
	"bytes"
	"encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"maps"
	"unicode/utf8"
)

var (
	ErrNilMap       = errors.New("metadata: nil map")
	ErrEmptyKey     = errors.New("metadata: empty key")
	ErrInvalidValue = errors.New("metadata: invalid JSON value")
)

// Map stores encoded JSON extension values. Its zero value is writable through
// Set. Clone and Merge copy bytes; Merge validates both sides before mutation.
// Equal compares encoded forms without semantic normalization. JSON preserves
// nil as null and an explicitly empty map as an object.
type Map map[string]json.RawMessage

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
	numbers := jsonv2.UnmarshalFromFunc(func(decoder *jsontext.Decoder, value *any) error {
		if decoder.PeekKind() != jsontext.KindNumber {
			return errors.ErrUnsupported
		}
		number, err := decoder.ReadValue()
		if err != nil {
			return err
		}
		*value = json.Number(number)
		return nil
	})
	var value any
	if err := jsonv2.Unmarshal(raw, &value, jsonv2.WithUnmarshalers(numbers)); err != nil {
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

	if !utf8.ValidString(key) {
		return fmt.Errorf("metadata: key %q: %w", key, ErrInvalidValue)
	}
	encoded, err := jsonv2.Marshal(value, jsonv2.Deterministic(true))
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
	if !utf8.ValidString(key) || !jsontext.Value(raw).IsValid() {
		return zero, true, fmt.Errorf("metadata: decode %q: %w", key, ErrInvalidValue)
	}

	var value T
	if err := jsonv2.Unmarshal(raw, &value); err != nil {
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
		if !utf8.ValidString(key) || !jsontext.Value(value).IsValid() {
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
	return jsonv2.Marshal(wireMap(m), jsonv2.Deterministic(true), jsonv2.FormatNilMapAsNull(true))
}

func (m *Map) UnmarshalJSON(data []byte) error {
	if m == nil {
		return errors.New("metadata: map receiver is nil")
	}

	var decoded map[string]json.RawMessage
	if err := jsonv2.Unmarshal(data, &decoded); err != nil {
		return fmt.Errorf("metadata: decode map: %w", err)
	}
	candidate := Map(decoded)
	if err := candidate.Validate(); err != nil {
		return err
	}
	*m = candidate
	return nil
}
