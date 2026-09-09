package chat

import (
	"bytes"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"regexp"
)

// ErrInvalidToolDefinition identifies a model-facing tool schema that cannot be
// transported safely.
var ErrInvalidToolDefinition = errors.New("chat: invalid tool definition")

var toolNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// ToolDefinition is the serializable description exposed to a model. Tool
// execution belongs to package tool and is deliberately absent here.
type ToolDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

func (t ToolDefinition) Clone() ToolDefinition {
	t.InputSchema = bytes.Clone(t.InputSchema)
	return t
}

func (t ToolDefinition) Validate() error {
	if !toolNamePattern.MatchString(t.Name) {
		return fmt.Errorf("%w: name must match %s", ErrInvalidToolDefinition, toolNamePattern)
	}
	var schema map[string]json.RawMessage
	if len(t.InputSchema) == 0 {
		return fmt.Errorf("%w: missing input schema", ErrInvalidToolDefinition)
	}
	if err := json.Unmarshal(t.InputSchema, &schema); err != nil || schema == nil {
		return fmt.Errorf("%w: input schema must be a JSON object", ErrInvalidToolDefinition)
	}
	var schemaType string
	if err := json.Unmarshal(schema["type"], &schemaType); err != nil || schemaType != "object" {
		return fmt.Errorf("%w: input schema type must be %q", ErrInvalidToolDefinition, "object")
	}
	return nil
}

func (t ToolDefinition) MarshalJSON() ([]byte, error) {
	if err := t.Validate(); err != nil {
		return nil, err
	}
	type wireToolDefinition ToolDefinition
	return json.Marshal(wireToolDefinition(t))
}

func (t *ToolDefinition) UnmarshalJSON(data []byte) error {
	if t == nil {
		return fmt.Errorf("%w: nil receiver", ErrInvalidToolDefinition)
	}
	type wireToolDefinition ToolDefinition
	var decoded wireToolDefinition
	if err := jsonv2.Unmarshal(data, &decoded, jsonv2.RejectUnknownMembers(true)); err != nil {
		return fmt.Errorf("%w: decode: %w", ErrInvalidToolDefinition, err)
	}
	candidate := ToolDefinition(decoded)
	if err := candidate.Validate(); err != nil {
		return err
	}
	*t = candidate
	return nil
}
