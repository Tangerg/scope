package metadata

import (
	jsonv2 "encoding/json/v2"
	"fmt"
	"strings"
	"unicode"
)

// Extensions is the JSON-only, namespaced provider extension value used by
// protocol options. It owns both the namespace/name key policy and encoded
// value ownership, so invalid extension state cannot be constructed through a
// public map.
type Extensions struct {
	values Map
}

func (e *Extensions) Set(key string, value any) error {
	if e == nil {
		return ErrNilMap
	}
	if !validExtensionKey(key) {
		return fmt.Errorf("metadata: extension key %q must use namespace/name", key)
	}
	return e.values.Set(key, value)
}

func (e Extensions) Decode[T any](key string) (T, bool, error) {
	if !validExtensionKey(key) {
		var zero T
		return zero, false, fmt.Errorf("metadata: extension key %q must use namespace/name", key)
	}
	return e.values.Decode[T](key)
}

func (e *Extensions) Merge(source Extensions) error {
	if e == nil {
		return ErrNilMap
	}
	if err := source.Validate(); err != nil {
		return fmt.Errorf("metadata: merge extensions: %w", err)
	}
	return e.values.Merge(source.values)
}

func (e Extensions) Clone() Extensions {
	return Extensions{values: e.values.Clone()}
}

func (e Extensions) Equal(other Extensions) bool {
	return e.values.Equal(other.values)
}

func (e Extensions) IsZero() bool { return len(e.values) == 0 }

func (e Extensions) Validate() error {
	for key := range e.values {
		if !validExtensionKey(key) {
			return fmt.Errorf("metadata: extension key %q must use namespace/name", key)
		}
	}
	return e.values.Validate()
}

func (e Extensions) MarshalJSON() ([]byte, error) {
	if err := e.Validate(); err != nil {
		return nil, err
	}
	return jsonv2.Marshal(e.values, jsonv2.Deterministic(true))
}

func (e *Extensions) UnmarshalJSON(data []byte) error {
	if e == nil {
		return ErrNilMap
	}
	var values Map
	if err := jsonv2.Unmarshal(data, &values); err != nil {
		return fmt.Errorf("metadata: decode extensions: %w", err)
	}
	candidate := Extensions{values: values}
	if err := candidate.Validate(); err != nil {
		return err
	}
	*e = candidate
	return nil
}

func validExtensionKey(key string) bool {
	if strings.Count(key, "/") != 1 {
		return false
	}
	namespace, name, _ := strings.Cut(key, "/")
	return validExtensionSegment(namespace) && validExtensionSegment(name)
}

func validExtensionSegment(segment string) bool {
	if segment == "" {
		return false
	}
	for _, r := range segment {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '.' || r == '_' || r == '-' {
			continue
		}
		return false
	}
	return true
}
