package agent

import (
	"encoding/json"
	"errors"
	"fmt"

	corejsonschema "github.com/Tangerg/scope/core/jsonschema"
)

var ErrInvalidSchema = errors.New("agent: invalid schema")

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

func (s Schema) ValidateInput(input Input) error {
	if err := s.validate(input.data); err != nil {
		return fmt.Errorf("%w: schema validation: %w", ErrInvalidInput, err)
	}
	return nil
}

func (s Schema) ValidateOutput(output Output) error {
	if err := s.validate(output.data); err != nil {
		return fmt.Errorf("%w: schema validation: %w", ErrInvalidOutput, err)
	}
	return nil
}

func (s Schema) validate(data []byte) error {
	if !s.Valid() {
		return ErrInvalidSchema
	}
	return s.contract.Validate(data)
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
