package chat

import (
	"bytes"
	"encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"fmt"
	"strings"
)

// ToolCall is one complete, untrusted model proposal to invoke a named tool.
// Arguments retains the provider's JSON text so malformed final model output
// remains serializable. A runtime must promote the proposal through the bound
// Tool schema before exposing it to capabilities or execution.
type ToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments,omitempty"`
}

// ToolCallDelta is one streaming fragment of a ToolCall. It cannot appear in a
// Request; ResponseAccumulator is the only boundary that promotes fragments to
// a complete ToolCall.
type ToolCallDelta struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments,omitempty"`
}

func (t ToolCallDelta) Validate() error {
	if t.ID == "" {
		return fmt.Errorf("%w: delta ID must not be empty", ErrInvalidToolCall)
	}
	if t.Name == "" {
		return fmt.Errorf("%w: delta name must not be empty", ErrInvalidToolCall)
	}
	return nil
}

// ToolOutput is the provider-neutral value produced by a Tool. Content is the
// model-visible ordered text/media representation. Details is optional JSON
// for structured consumers; providers use its encoded JSON as the model-visible
// fallback only when Content is empty.
//
// Content deliberately reuses Part so media has one representation across the
// chat protocol. Only text and media parts are valid here; reasoning, calls,
// refusals, and results are rejected.
type ToolOutput struct {
	Content []Part          `json:"content,omitempty"`
	Details json.RawMessage `json:"details,omitempty"`
}

// NewTextToolOutput returns a text output. Empty text is represented by the
// valid zero ToolOutput rather than an invalid empty text Part.
func NewTextToolOutput(text string) ToolOutput {
	if text == "" {
		return ToolOutput{}
	}
	return ToolOutput{Content: []Part{NewTextPart(text)}}
}

// NewJSONToolOutput returns a structured output whose exact JSON encoding is
// preserved. The value must be one complete RFC 7493 JSON document.
func NewJSONToolOutput(value json.RawMessage) (ToolOutput, error) {
	output := ToolOutput{Details: bytes.Clone(value)}
	if err := output.Validate(); err != nil {
		return ToolOutput{}, err
	}
	return output, nil
}

func (t ToolOutput) Clone() ToolOutput {
	clone := ToolOutput{Details: bytes.Clone(t.Details)}
	if t.Content != nil {
		clone.Content = make([]Part, len(t.Content))
		for index := range t.Content {
			clone.Content[index] = t.Content[index].Clone()
		}
	}
	return clone
}

func (t ToolOutput) MarshalJSON() ([]byte, error) {
	if err := t.Validate(); err != nil {
		return nil, err
	}
	type wireToolOutput ToolOutput
	return json.Marshal(wireToolOutput(t))
}

func (t *ToolOutput) UnmarshalJSON(data []byte) error {
	if t == nil {
		return fmt.Errorf("%w: nil receiver", ErrInvalidToolOutput)
	}
	type wireToolOutput ToolOutput
	var decoded wireToolOutput
	if err := jsonv2.Unmarshal(data, &decoded, jsonv2.RejectUnknownMembers(true)); err != nil {
		return fmt.Errorf("%w: decode: %w", ErrInvalidToolOutput, err)
	}
	candidate := ToolOutput(decoded)
	if err := candidate.Validate(); err != nil {
		return err
	}
	*t = candidate
	return nil
}

func (t ToolOutput) Validate() error {
	for index := range t.Content {
		part := t.Content[index]
		if part.Kind != PartText && part.Kind != PartMedia {
			return fmt.Errorf("%w: content[%d]: unsupported part kind %q", ErrInvalidToolOutput, index, part.Kind)
		}
		if err := part.Validate(); err != nil {
			return fmt.Errorf("%w: content[%d]: %w", ErrInvalidToolOutput, index, err)
		}
	}
	if len(t.Details) != 0 && !jsontext.Value(t.Details).IsValid() {
		return fmt.Errorf("%w: details must be one valid RFC 7493 JSON document", ErrInvalidToolOutput)
	}
	return nil
}

// Text returns the lossless text projection used by providers whose tool
// result protocol accepts only strings. It reports false when Content contains
// media so adapters cannot silently discard it. Details is encoded only when
// Content is empty.
func (t ToolOutput) Text() (string, bool) {
	if len(t.Content) == 0 {
		return string(t.Details), true
	}
	var projected strings.Builder
	for index := range t.Content {
		if t.Content[index].Kind != PartText {
			return "", false
		}
		projected.WriteString(t.Content[index].Text)
	}
	return projected.String(), true
}

func (t ToolCall) Validate() error {
	if t.ID == "" {
		return fmt.Errorf("%w: ID must not be empty", ErrInvalidToolCall)
	}
	if t.Name == "" {
		return fmt.Errorf("%w: name must not be empty", ErrInvalidToolCall)
	}
	return nil
}

// ToolResult is one tool execution result correlated to a ToolCall by ID.
type ToolResult struct {
	ID      string     `json:"id"`
	Name    string     `json:"name"`
	Output  ToolOutput `json:"output"`
	IsError bool       `json:"is_error,omitempty"`
}

func (t ToolResult) Clone() ToolResult {
	clone := t
	clone.Output = t.Output.Clone()
	return clone
}

func (t ToolResult) Validate() error {
	if t.ID == "" {
		return fmt.Errorf("%w: ID must not be empty", ErrInvalidToolResult)
	}
	if t.Name == "" {
		return fmt.Errorf("%w: name must not be empty", ErrInvalidToolResult)
	}
	if err := t.Output.Validate(); err != nil {
		return fmt.Errorf("%w: output: %w", ErrInvalidToolResult, err)
	}
	return nil
}
