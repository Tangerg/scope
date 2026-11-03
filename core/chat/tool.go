package chat

import (
	"fmt"
	"slices"
	"unicode/utf8"

	corecontent "github.com/Tangerg/scope/core/content"
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

// ToolResult answers one ToolCall. IsError is the only representation of an
// unsuccessful result: producers write the explanation into Output, and a
// protocol without an error flag renders the flag through MarkedOutput.
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

// toolErrorMarker carries IsError in-band where the protocol has no flag for it.
const toolErrorMarker = "error: "

// MarkedOutput is the Output a protocol without a tool-error flag sends: an
// error result leads with a text marker so the model can still tell it from a
// successful one. Structured details become text because they would otherwise
// follow the marker as an ignored fallback.
func (t ToolResult) MarkedOutput() toolcontract.Output {
	output := t.Output.Clone()
	if !t.IsError {
		return output
	}
	if len(output.Content) == 0 && len(output.Details) != 0 {
		output.Content = []corecontent.Block{{Kind: corecontent.Text, Text: string(output.Details)}}
	}
	output.Content = slices.Insert(output.Content, 0, corecontent.Block{Kind: corecontent.Text, Text: toolErrorMarker})
	return output
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
