package chat

import (
	"bytes"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	corejsonschema "github.com/Tangerg/scope/core/jsonschema"
)

var (
	ErrInvalidOutputFormat = errors.New("chat: invalid output format")
	// ErrUnsupportedOutputFormat lets a provider reject a valid portable format
	// that its native endpoint cannot enforce.
	ErrUnsupportedOutputFormat = errors.New("chat: unsupported output format")
)

type OutputFormatType string

const (
	OutputFormatText       OutputFormatType = "text"
	OutputFormatJSON       OutputFormatType = "json"
	OutputFormatJSONSchema OutputFormatType = "json_schema"
)

// OutputFormat is the requested representation. Name, Description, and Schema
// belong only to OutputFormatJSONSchema. Construction and cloning snapshot Schema.
type OutputFormat struct {
	Type        OutputFormatType `json:"type"`
	Name        string           `json:"name,omitempty"`
	Description string           `json:"description,omitempty"`
	Schema      json.RawMessage  `json:"schema,omitzero"`
}

func NewOutputFormat(formatType OutputFormatType) (OutputFormat, error) {
	format := OutputFormat{Type: formatType}
	if err := format.Validate(); err != nil {
		return OutputFormat{}, fmt.Errorf("chat: create output format: %w", err)
	}
	return format, nil
}

type JSONSchemaConfig struct {
	Name        string
	Description string
	Schema      json.RawMessage
}

func NewJSONSchemaOutputFormat(config JSONSchemaConfig) (OutputFormat, error) {
	format := OutputFormat{
		Type:        OutputFormatJSONSchema,
		Name:        config.Name,
		Description: config.Description,
		Schema:      bytes.Clone(config.Schema),
	}
	if err := format.Validate(); err != nil {
		return OutputFormat{}, fmt.Errorf("chat: create JSON Schema output format: %w", err)
	}
	return format, nil
}

func (o *OutputFormat) Clone() *OutputFormat {
	if o == nil {
		return nil
	}
	clone := *o
	clone.Schema = bytes.Clone(o.Schema)
	return &clone
}

func (o *OutputFormat) SchemaAs[T any]() (T, error) {
	var zero T
	if o == nil {
		return zero, fmt.Errorf("%w: nil receiver", ErrInvalidOutputFormat)
	}
	if err := o.Validate(); err != nil {
		return zero, err
	}
	if o.Type != OutputFormatJSONSchema {
		return zero, fmt.Errorf("%w: %s output has no schema", ErrInvalidOutputFormat, o.Type)
	}
	var schema T
	if err := jsonv2.Unmarshal(o.Schema, &schema); err != nil {
		return zero, fmt.Errorf("chat: decode output schema: %w", err)
	}
	return schema, nil
}

func (o OutputFormat) Validate() error {
	switch o.Type {
	case OutputFormatText, OutputFormatJSON:
		if o.Name != "" || o.Description != "" || len(o.Schema) != 0 {
			return fmt.Errorf("%w: %s output must not define schema fields", ErrInvalidOutputFormat, o.Type)
		}
		return nil
	case OutputFormatJSONSchema:
		return o.validateJSONSchema()
	default:
		return fmt.Errorf("%w: unsupported type %q", ErrInvalidOutputFormat, o.Type)
	}
}

func (o OutputFormat) validateJSONSchema() error {
	if !utf8.ValidString(o.Name) || !utf8.ValidString(o.Description) {
		return fmt.Errorf("%w: json_schema name and description must be valid UTF-8", ErrInvalidOutputFormat)
	}
	if o.Name == "" {
		return fmt.Errorf("%w: json_schema name must not be empty", ErrInvalidOutputFormat)
	}
	if strings.TrimSpace(o.Name) != o.Name {
		return fmt.Errorf("%w: json_schema name must not have surrounding whitespace", ErrInvalidOutputFormat)
	}
	if strings.TrimSpace(o.Description) != o.Description {
		return fmt.Errorf("%w: json_schema description must not have surrounding whitespace", ErrInvalidOutputFormat)
	}
	schema, err := corejsonschema.Parse(o.Schema)
	if err != nil {
		return fmt.Errorf("%w: json_schema schema: %w", ErrInvalidOutputFormat, err)
	}
	var object map[string]json.RawMessage
	if jsonv2.Unmarshal(schema.JSON(), &object) != nil || object == nil {
		return fmt.Errorf("%w: json_schema schema must be an object", ErrInvalidOutputFormat)
	}
	return nil
}

func (o OutputFormat) MarshalJSON() ([]byte, error) {
	if err := o.Validate(); err != nil {
		return nil, err
	}
	type wireOutputFormat OutputFormat
	return jsonv2.Marshal(wireOutputFormat(o), jsonv2.Deterministic(true))
}

func (o *OutputFormat) UnmarshalJSON(data []byte) error {
	if o == nil {
		return fmt.Errorf("%w: nil receiver", ErrInvalidOutputFormat)
	}
	type wireOutputFormat OutputFormat
	var decoded wireOutputFormat
	if err := jsonv2.Unmarshal(data, &decoded, jsonv2.RejectUnknownMembers(true)); err != nil {
		return fmt.Errorf("%w: decode: %w", ErrInvalidOutputFormat, err)
	}
	candidate := OutputFormat(decoded)
	if err := candidate.Validate(); err != nil {
		return err
	}
	*o = candidate
	return nil
}
