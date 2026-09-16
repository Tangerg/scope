package agent

import (
	"encoding/json"
	"errors"
	"fmt"

	corejsonschema "github.com/Tangerg/scope/core/jsonschema"
)

var (
	ErrInvalidSchema    = errors.New("agent: invalid schema")
	ErrSchemaValidation = errors.New("agent: schema validation failed")
)

// Schema is an immutable, resolved JSON Schema used by Framework input and
// output contracts. Its zero value is invalid.
type Schema struct {
	contract corejsonschema.Schema
}

// ParseSchema validates and resolves one JSON Schema.
func ParseSchema(data json.RawMessage) (Schema, error) {
	contract, err := corejsonschema.Parse(data)
	if err != nil {
		return Schema{}, fmt.Errorf("%w: %w", ErrInvalidSchema, err)
	}
	return Schema{contract: contract}, nil
}

// SchemaFor derives and resolves a JSON Schema for T. Named types contribute
// package-qualified schema names, so relocating them can change Descriptor
// digests and exact Deployment bindings even when their JSON fields stay equal.
func SchemaFor[T any]() (Schema, error) {
	contract, err := corejsonschema.For[T]()
	if err != nil {
		return Schema{}, fmt.Errorf("%w: %w", ErrInvalidSchema, err)
	}
	return Schema{contract: contract}, nil
}

// JSON returns an independently owned JSON representation.
func (s Schema) JSON() json.RawMessage { return s.contract.JSON() }

func (s Schema) Valid() bool { return s.contract.Valid() }

// Validate checks one JSON value against this schema, independently of its role.
// Invalid schemas return ErrInvalidSchema; rejected values return ErrSchemaValidation.
func (s Schema) Validate(value json.RawMessage) error {
	if !s.Valid() {
		return ErrInvalidSchema
	}
	if err := s.contract.Validate(value); err != nil {
		return fmt.Errorf("%w: %w", ErrSchemaValidation, err)
	}
	return nil
}

func (s Schema) MarshalJSON() ([]byte, error) {
	if !s.Valid() {
		return nil, ErrInvalidSchema
	}
	return s.contract.JSON(), nil
}

func (s *Schema) UnmarshalJSON(data []byte) error {
	if s == nil {
		return fmt.Errorf("%w: nil receiver", ErrInvalidSchema)
	}
	value, err := ParseSchema(data)
	if err != nil {
		return err
	}
	*s = value
	return nil
}
