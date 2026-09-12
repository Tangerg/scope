package chat

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"fmt"
	"slices"

	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/core/metadata"
)

// PartKind identifies which payload in Part is active.
type PartKind string

const (
	// PartText carries plain text.
	PartText PartKind = "text"
	// PartMedia carries an image, audio, document, or other media value.
	PartMedia PartKind = "media"
	// PartReasoning carries visible reasoning and optional opaque replay state.
	PartReasoning PartKind = "reasoning"
	// PartToolCall carries one tool invocation request.
	PartToolCall PartKind = "tool_call"
	// PartToolResult carries one tool execution result.
	PartToolResult PartKind = "tool_result"
	// PartRefusal carries a model refusal separately from ordinary output text.
	PartRefusal PartKind = "refusal"
)

func (p PartKind) Valid() bool {
	switch p {
	case PartText, PartMedia, PartReasoning, PartToolCall, PartToolResult, PartRefusal:
		return true
	default:
		return false
	}
}

// Part is a tagged protocol value. Kind selects exactly one payload shape:
// Text, Media, reasoning Text/ReasoningState, ToolCall, or ToolResult. Citations
// annotate only text parts. Metadata retains JSON-safe, part-scoped provider
// state without weakening the common semantic payload.
type Part struct {
	Kind           PartKind     `json:"kind"`
	Text           string       `json:"text,omitempty"`
	Media          *media.Media `json:"media,omitempty"`
	ReasoningState []byte       `json:"reasoning_state,omitempty"`
	ToolCall       *ToolCall    `json:"tool_call,omitempty"`
	ToolResult     *ToolResult  `json:"tool_result,omitempty"`
	Citations      []Citation   `json:"citations,omitempty"`
	Metadata       metadata.Map `json:"metadata,omitzero"`
}

type partPayload uint8

const (
	payloadText partPayload = 1 << iota
	payloadMedia
	payloadReasoningState
	payloadToolCall
	payloadToolResult
)

func (p Part) Clone() Part {
	clone := p
	clone.ReasoningState = slices.Clone(p.ReasoningState)
	clone.Media = p.Media.Clone()
	clone.Metadata = p.Metadata.Clone()
	clone.Citations = slices.Clone(p.Citations)
	if p.ToolCall != nil {
		clone.ToolCall = new(*p.ToolCall)
	}
	if p.ToolResult != nil {
		result := p.ToolResult.Clone()
		clone.ToolResult = &result
	}
	return clone
}

// NewTextPart establishes the text payload shape; aggregate validation rejects
// empty text.
func NewTextPart(text string) Part {
	return Part{Kind: PartText, Text: text}
}

// NewMediaPart borrows value. Use Clone when independent ownership is needed.
func NewMediaPart(value *media.Media) Part {
	return Part{Kind: PartMedia, Media: value}
}

// NewReasoningPart copies opaque replay state alongside visible reasoning.
func NewReasoningPart(text string, state []byte) Part {
	return Part{Kind: PartReasoning, Text: text, ReasoningState: slices.Clone(state)}
}

// NewToolCallPart preserves a model-requested invocation as typed assistant
// output.
func NewToolCallPart(call ToolCall) Part {
	return Part{Kind: PartToolCall, ToolCall: new(call)}
}

// NewToolResultPart copies result and borrows its nested content.
func NewToolResultPart(result ToolResult) Part {
	return Part{Kind: PartToolResult, ToolResult: new(result)}
}

// NewRefusalPart keeps a model refusal distinguishable from ordinary output.
func NewRefusalPart(text string) Part {
	return Part{Kind: PartRefusal, Text: text}
}

func (p Part) Validate() error {
	if !p.Kind.Valid() {
		return fmt.Errorf("%w: unknown kind %q", ErrInvalidPart, p.Kind)
	}
	if len(p.Citations) != 0 && p.Kind != PartText {
		return fmt.Errorf("%w: kind %q cannot carry citations", ErrInvalidPart, p.Kind)
	}
	if err := p.Metadata.Validate(); err != nil {
		return fmt.Errorf("%w: metadata: %w", ErrInvalidPart, err)
	}

	payload := p.payload()
	switch p.Kind {
	case PartText:
		return p.validateTextPayload(payload)
	case PartMedia:
		return p.validateMediaPayload(payload)
	case PartReasoning:
		return p.validateReasoningPayload(payload)
	case PartToolCall:
		return p.validatePayload(payload, payloadToolCall, func() error { return p.ToolCall.Validate() })
	case PartToolResult:
		return p.validatePayload(payload, payloadToolResult, func() error { return p.ToolResult.Validate() })
	case PartRefusal:
		return p.validateRefusalPayload(payload)
	}
	return nil
}

func (p Part) payload() partPayload {
	var payload partPayload
	if p.Text != "" {
		payload |= payloadText
	}
	if p.Media != nil {
		payload |= payloadMedia
	}
	if len(p.ReasoningState) != 0 {
		payload |= payloadReasoningState
	}
	if p.ToolCall != nil {
		payload |= payloadToolCall
	}
	if p.ToolResult != nil {
		payload |= payloadToolResult
	}
	return payload
}

func (p Part) validateTextPayload(payload partPayload) error {
	if payload != payloadText {
		return fmt.Errorf("%w: kind %q requires non-empty text and no other payload", ErrInvalidPart, p.Kind)
	}
	for index := range p.Citations {
		if err := p.Citations[index].Validate(); err != nil {
			return fmt.Errorf("%w: citations[%d]: %w", ErrInvalidPart, index, err)
		}
	}
	return nil
}

func (p Part) validateMediaPayload(payload partPayload) error {
	if payload != payloadMedia {
		return fmt.Errorf("%w: kind %q requires media and no other payload", ErrInvalidPart, p.Kind)
	}
	if err := p.Media.Validate(); err != nil {
		return fmt.Errorf("%w: media: %w", ErrInvalidPart, err)
	}
	return nil
}

func (p Part) validateReasoningPayload(payload partPayload) error {
	const allowed = payloadText | payloadReasoningState
	if payload == 0 || payload&^allowed != 0 {
		return fmt.Errorf("%w: kind %q requires text or reasoning state and no other payload", ErrInvalidPart, p.Kind)
	}
	return nil
}

func (p Part) validateRefusalPayload(payload partPayload) error {
	if payload != payloadText {
		return fmt.Errorf("%w: kind %q requires non-empty text and no other payload", ErrInvalidPart, p.Kind)
	}
	return nil
}

func (p Part) MarshalJSON() ([]byte, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	type wirePart Part
	return json.Marshal(wirePart(p))
}

func (p *Part) UnmarshalJSON(data []byte) error {
	if p == nil {
		return fmt.Errorf("%w: nil receiver", ErrInvalidPart)
	}
	type wirePart Part
	var decoded wirePart
	if err := jsonv2.Unmarshal(data, &decoded, jsonv2.RejectUnknownMembers(true)); err != nil {
		return fmt.Errorf("%w: decode: %w", ErrInvalidPart, err)
	}
	candidate := Part(decoded)
	if err := candidate.Validate(); err != nil {
		return err
	}
	*p = candidate
	return nil
}

func (p Part) validatePayload(actual, required partPayload, validate func() error) error {
	if actual != required {
		return fmt.Errorf("%w: kind %q requires its matching payload and no other payload", ErrInvalidPart, p.Kind)
	}
	if err := validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidPart, err)
	}
	return nil
}
