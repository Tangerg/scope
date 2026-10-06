package rag

import (
	"errors"
	"fmt"
	"maps"
	"reflect"
	"strings"
	"unicode/utf8"

	"github.com/samber/lo"
)

var (
	// ErrInvalidQuery identifies invalid text or typed values.
	ErrInvalidQuery = errors.New("rag: invalid query")
	ErrNilRetriever = errors.New("rag: retriever must not be nil")
	// ErrInvalidQueryValueKey identifies a zero or malformed typed slot.
	ErrInvalidQueryValueKey = errors.New("rag: invalid query value key")
	ErrNilQueryValue        = errors.New("rag: query value must not be nil")
)

var queryAnyType = reflect.TypeFor[any]()

type valueKeyIdentity struct {
	name string
}

// ValueKey is a typed slot in a [Query]. Define a key once and share that key
// with the code that writes and reads the slot. Each key instance has its own
// identity; the name is diagnostic only and never acts as a global namespace.
//
// The zero value and ValueKey[any] are invalid. Create keys with [NewValueKey]
// and retain the returned value because equal names do not share identity.
type ValueKey[T any] struct {
	identity *valueKeyIdentity
}

func NewValueKey[T any](name string) (ValueKey[T], error) {
	key := ValueKey[T]{identity: &valueKeyIdentity{name: name}}
	if err := key.validate(); err != nil {
		return ValueKey[T]{}, err
	}
	return key, nil
}

func (v ValueKey[T]) Name() string {
	if v.identity == nil {
		return ""
	}
	return v.identity.name
}

func (v ValueKey[T]) validate() error {
	switch {
	case v.identity == nil,
		v.identity.name == "",
		v.identity.name != strings.TrimSpace(v.identity.name):
		return fmt.Errorf(
			"%w: name must be non-empty without surrounding whitespace",
			ErrInvalidQueryValueKey,
		)
	case reflect.TypeFor[T]() == queryAnyType:
		return fmt.Errorf("%w: value type must not be any", ErrInvalidQueryValueKey)
	default:
		return nil
	}
}

// Query is a persistent retrieval query: Text is required, and WithText or
// WithValue returns a new envelope without changing existing queries' slots.
// Referenced values remain caller-owned and must be treated as read-only when
// the same query is used by parallel retrieval stages.
type Query struct {
	text   string
	values map[*valueKeyIdentity]any
}

// NewQuery trims surrounding whitespace and requires valid UTF-8 text.
func NewQuery(text string) (Query, error) {
	query := Query{text: strings.TrimSpace(text)}
	if err := query.Validate(); err != nil {
		return Query{}, err
	}
	return query, nil
}

func (q Query) Validate() error {
	if !utf8.ValidString(q.text) {
		return fmt.Errorf("%w: text must be valid UTF-8", ErrInvalidQuery)
	}
	if q.text == "" || q.text != strings.TrimSpace(q.text) {
		return fmt.Errorf("%w: text must be non-empty without surrounding whitespace", ErrInvalidQuery)
	}
	return nil
}

func (q Query) Text() string { return q.text }

// Value returns the value stored under key. Missing values are distinct from
// invalid keys.
func (q Query) Value[T any](key ValueKey[T]) (T, bool, error) {
	var zero T
	if err := q.Validate(); err != nil {
		return zero, false, err
	}
	if err := key.validate(); err != nil {
		return zero, false, err
	}
	entry, found := q.values[key.identity]
	if !found {
		return zero, false, nil
	}
	value, ok := entry.(T)
	if !ok {
		return zero, false, fmt.Errorf("%w: corrupt value for %q", ErrInvalidQuery, key.Name())
	}
	return value, true, nil
}

func (q Query) WithValue[T any](key ValueKey[T], value T) (Query, error) {
	if err := q.Validate(); err != nil {
		return Query{}, err
	}
	if err := key.validate(); err != nil {
		return Query{}, err
	}
	if lo.IsNil(value) {
		return Query{}, fmt.Errorf("%w for %q", ErrNilQueryValue, key.Name())
	}
	clone := Query{text: q.text, values: maps.Clone(q.values)}
	if clone.values == nil {
		clone.values = make(map[*valueKeyIdentity]any)
	}
	clone.values[key.identity] = value
	return clone, nil
}

func (q Query) WithText(text string) (Query, error) {
	if err := q.Validate(); err != nil {
		return Query{}, err
	}
	updated, err := NewQuery(text)
	if err != nil {
		return Query{}, err
	}
	updated.values = q.values
	return updated, nil
}
