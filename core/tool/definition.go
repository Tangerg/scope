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
)

var ErrInvalidDefinition = errors.New("tool: invalid definition")

// Definition describes a tool independently of any model naming or transport convention.
type Definition struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

func (d Definition) Clone() Definition {
	d.InputSchema = bytes.Clone(d.InputSchema)
	return d
}

func (d Definition) Validate() error {
	if d.Name == "" || !utf8.ValidString(d.Name) || strings.ContainsFunc(d.Name, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
		return fmt.Errorf("%w: tool name must be non-empty UTF-8 without whitespace or control characters", ErrInvalidDefinition)
	}
	if !utf8.ValidString(d.Description) {
		return fmt.Errorf("%w: description must be valid UTF-8", ErrInvalidDefinition)
	}
	var schema map[string]json.RawMessage
	if len(d.InputSchema) == 0 {
		return fmt.Errorf("%w: missing input schema", ErrInvalidDefinition)
	}
	if err := jsonv2.Unmarshal(d.InputSchema, &schema); err != nil || schema == nil {
		return fmt.Errorf("%w: input schema must be a JSON object", ErrInvalidDefinition)
	}
	var schemaType string
	if err := jsonv2.Unmarshal(schema["type"], &schemaType); err != nil || schemaType != "object" {
		return fmt.Errorf("%w: input schema type must be %q", ErrInvalidDefinition, "object")
	}
	return nil
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
