package tool

import (
	"bytes"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Tangerg/scope/core/jsonschema"
)

const inputSchemaType = "object"

var ErrInvalidDefinition = errors.New("tool: invalid definition")

// Definition is a detached wire snapshot. Validation and executable binding
// compile the same schema contract, so advertising a tool cannot bypass it.
type Definition struct {
	Name         string          `json:"name"`
	Description  string          `json:"description,omitempty"`
	InputSchema  json.RawMessage `json:"input_schema"`
	OutputSchema json.RawMessage `json:"output_schema,omitzero"`
	Effect       Effect          `json:"effect,omitzero"`
}

func (d Definition) Clone() Definition {
	d.InputSchema = bytes.Clone(d.InputSchema)
	d.OutputSchema = bytes.Clone(d.OutputSchema)
	return d
}

func (d Definition) Validate() error {
	_, err := d.compile()
	return err
}

func (d Definition) compile() (*contractState, error) {
	if !d.Effect.Valid() {
		return nil, fmt.Errorf("%w: unknown effect %q", ErrInvalidDefinition, d.Effect)
	}
	if d.Name == "" || !utf8.ValidString(d.Name) || strings.ContainsFunc(d.Name, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
		return nil, fmt.Errorf("%w: tool name must be non-empty UTF-8 without whitespace or control characters", ErrInvalidDefinition)
	}
	if !utf8.ValidString(d.Description) {
		return nil, fmt.Errorf("%w: description must be valid UTF-8", ErrInvalidDefinition)
	}
	input, err := jsonschema.Parse(d.InputSchema)
	if err != nil {
		return nil, fmt.Errorf("%w: input schema: %w", ErrInvalidDefinition, err)
	}
	var root struct {
		Type string `json:"type"`
	}
	if decodeErr := jsonv2.Unmarshal(input.JSON(), &root); decodeErr != nil || root.Type != inputSchemaType {
		return nil, fmt.Errorf("%w: input schema type must be %q", ErrInvalidDefinition, inputSchemaType)
	}
	var output jsonschema.Schema
	if d.OutputSchema != nil {
		output, err = jsonschema.Parse(d.OutputSchema)
		if err != nil {
			return nil, fmt.Errorf("%w: output schema: %w", ErrInvalidDefinition, err)
		}
	}
	return &contractState{name: d.Name, description: d.Description, input: input, output: output, effect: d.Effect}, nil
}

func (d Definition) MarshalJSON() ([]byte, error) {
	if err := d.Validate(); err != nil {
		return nil, err
	}
	return jsonv2.Marshal(wireDefinition(d), jsonv2.Deterministic(true))
}

func (d *Definition) UnmarshalJSON(data []byte) error {
	if d == nil {
		return fmt.Errorf("%w: nil receiver", ErrInvalidDefinition)
	}
	var decoded wireDefinition
	if err := jsonv2.Unmarshal(data, &decoded, jsonv2.RejectUnknownMembers(true)); err != nil {
		return fmt.Errorf("%w: decode: %w", ErrInvalidDefinition, err)
	}
	candidate := Definition(decoded)
	if err := candidate.Validate(); err != nil {
		return err
	}
	*d = candidate
	return nil
}

type wireDefinition Definition

func (Definition) JSONSchemaModel() any { return wireDefinition{} }
