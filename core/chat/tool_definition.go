package chat

import (
	"bytes"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

var ErrInvalidToolDefinition = errors.New("chat: invalid tool definition")

const maxToolNameLength = 64

var toolNamePattern = regexp.MustCompile(fmt.Sprintf(`^[A-Za-z0-9_-]{1,%d}$`, maxToolNameLength))

// SanitizeToolName coerces an arbitrary label into a name [ToolDefinition.Validate]
// accepts, so callers building one from an external source need not re-encode the
// constraint. It may return "", which Validate rejects: sanitizing is not validation.
func SanitizeToolName(label string) string {
	var b strings.Builder
	pendingUnderscore := false
	for _, r := range label {
		if b.Len() >= maxToolNameLength {
			break
		}
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
			pendingUnderscore = false
		default:
			if !pendingUnderscore {
				b.WriteByte('_')
				pendingUnderscore = true
			}
		}
	}
	return strings.Trim(b.String(), "_")
}

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
	if !utf8.ValidString(t.Description) {
		return fmt.Errorf("%w: description must be valid UTF-8", ErrInvalidToolDefinition)
	}
	var schema map[string]json.RawMessage
	if len(t.InputSchema) == 0 {
		return fmt.Errorf("%w: missing input schema", ErrInvalidToolDefinition)
	}
	if err := jsonv2.Unmarshal(t.InputSchema, &schema); err != nil || schema == nil {
		return fmt.Errorf("%w: input schema must be a JSON object", ErrInvalidToolDefinition)
	}
	var schemaType string
	if err := jsonv2.Unmarshal(schema["type"], &schemaType); err != nil || schemaType != "object" {
		return fmt.Errorf("%w: input schema type must be %q", ErrInvalidToolDefinition, "object")
	}
	return nil
}

func (t ToolDefinition) MarshalJSON() ([]byte, error) {
	if err := t.Validate(); err != nil {
		return nil, err
	}
	type wireToolDefinition ToolDefinition
	return jsonv2.Marshal(wireToolDefinition(t), jsonv2.Deterministic(true))
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
