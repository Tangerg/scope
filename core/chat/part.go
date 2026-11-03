package chat

import (
	jsonv2 "encoding/json/v2"
	"fmt"
	"slices"
	"unicode/utf8"

	corecontent "github.com/Tangerg/scope/core/content"
	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/core/metadata"
)

type PartKind string

const (
	PartText       PartKind = PartKind(corecontent.Text)
	PartMedia      PartKind = PartKind(corecontent.Media)
	PartReasoning  PartKind = "reasoning"
	PartToolCall   PartKind = "tool_call"
	PartToolResult PartKind = "tool_result"
	PartRefusal    PartKind = "refusal"
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
	Kind           PartKind               `json:"kind"`
	Text           string                 `json:"text,omitempty"`
	Media          *media.Media           `json:"media,omitzero"`
	ReasoningState []byte                 `json:"reasoning_state,omitempty"`
	ToolCall       *ToolCall              `json:"tool_call,omitzero"`
	ToolResult     *ToolResult            `json:"tool_result,omitzero"`
	Citations      []corecontent.Citation `json:"citations,omitempty"`
	Metadata       metadata.Map           `json:"metadata,omitzero"`
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

func NewToolCallPart(call ToolCall) Part {
	return Part{Kind: PartToolCall, ToolCall: new(call)}
}

// NewToolResultPart copies result and borrows its nested content.
func NewToolResultPart(result ToolResult) Part {
	return Part{Kind: PartToolResult, ToolResult: new(result)}
}

func NewRefusalPart(text string) Part {
	return Part{Kind: PartRefusal, Text: text}
}

// NewContentPart carries a content block, the shared text-or-media value chat
// parts and tool output both use; it borrows the block's nested values.
func NewContentPart(block corecontent.Block) Part {
	return Part{Kind: PartKind(block.Kind), Text: block.Text, Media: block.Media, Citations: block.Citations, Metadata: block.Metadata}
}

// Content projects a text or media part as the content block that owns its
// rules; chat control parts are not content.
func (p Part) Content() (corecontent.Block, bool) {
	if p.Kind != PartText && p.Kind != PartMedia {
		return corecontent.Block{}, false
	}
	return corecontent.Block{Kind: corecontent.Kind(p.Kind), Text: p.Text, Media: p.Media, Citations: p.Citations, Metadata: p.Metadata}, true
}

func (p Part) Validate() error {
	if block, isContent := p.Content(); isContent {
		if len(p.ReasoningState) > 0 || p.ToolCall != nil || p.ToolResult != nil {
			return fmt.Errorf("%w: content cannot carry chat control payloads", ErrInvalidPart)
		}
		if err := block.Validate(); err != nil {
			return fmt.Errorf("%w: %w", ErrInvalidPart, err)
		}
		return nil
	}
	if !utf8.ValidString(p.Text) {
		return fmt.Errorf("%w: text is not valid UTF-8", ErrInvalidPart)
	}
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
	return jsonv2.Marshal(wirePart(p), jsonv2.Deterministic(true))
}

func (p *Part) UnmarshalJSON(data []byte) error {
	if p == nil {
		return fmt.Errorf("%w: nil receiver", ErrInvalidPart)
	}
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

type wirePart Part

func (Part) JSONSchemaModel() any { return wirePart{} }
