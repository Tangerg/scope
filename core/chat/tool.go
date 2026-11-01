package chat

import (
	"fmt"
	"unicode/utf8"

	toolcontract "github.com/Tangerg/scope/core/tool"
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
	return ToolCall(t).Validate()
}

func (t ToolCall) Request() toolcontract.Request {
	return toolcontract.Request{Name: t.Name, Arguments: t.Arguments}
}

func (t ToolCall) Validate() error {
	if t.ID == "" {
		return fmt.Errorf("%w: ID must not be empty", ErrInvalidToolCall)
	}
	if !utf8.ValidString(t.ID) {
		return fmt.Errorf("%w: ID must be valid UTF-8", ErrInvalidToolCall)
	}
	if err := t.Request().Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidToolCall, err)
	}
	return nil
}

type ToolResult struct {
	ID      string              `json:"id"`
	Name    string              `json:"name"`
	Output  toolcontract.Output `json:"output"`
	IsError bool                `json:"is_error,omitzero"`
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
	if !utf8.ValidString(t.ID) || !utf8.ValidString(t.Name) {
		return fmt.Errorf("%w: ID and name must be valid UTF-8", ErrInvalidToolResult)
	}
	if err := t.Output.Validate(); err != nil {
		return fmt.Errorf("%w: output: %w", ErrInvalidToolResult, err)
	}
	return nil
}
