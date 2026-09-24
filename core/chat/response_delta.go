package chat

import (
	jsonv2 "encoding/json/v2"
	"fmt"
	"slices"
	"strings"

	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/core/metadata"
)

// PartDeltaKind identifies transport increments whose lifecycle differs from a
// complete Part, including incomplete tool arguments and citation attachment.
type PartDeltaKind string

// Delta kinds name the only payload shape active in each stream increment.
const (
	PartDeltaText      PartDeltaKind = "text"
	PartDeltaMedia     PartDeltaKind = "media"
	PartDeltaReasoning PartDeltaKind = "reasoning"
	PartDeltaToolCall  PartDeltaKind = "tool_call"
	PartDeltaCitation  PartDeltaKind = "citation"
	PartDeltaRefusal   PartDeltaKind = "refusal"
)

// deltaPayload names the optional slots an increment can carry. A kind allows
// a subset, so one exclusion rule replaces restating per kind which of the
// other slots must stay empty.
type deltaPayload uint8

const (
	deltaPayloadText deltaPayload = 1 << iota
	deltaPayloadMedia
	deltaPayloadReasoningState
	deltaPayloadToolCall
	deltaPayloadCitation
)

// A kind that allows no payload is not a kind, so this answers validity too
// rather than letting a second switch drift from this one.
func (p PartDeltaKind) allowedPayloads() (deltaPayload, bool) {
	switch p {
	case PartDeltaText, PartDeltaRefusal:
		return deltaPayloadText, true
	case PartDeltaMedia:
		return deltaPayloadMedia, true
	case PartDeltaReasoning:
		return deltaPayloadText | deltaPayloadReasoningState, true
	case PartDeltaToolCall:
		return deltaPayloadToolCall, true
	case PartDeltaCitation:
		return deltaPayloadCitation, true
	default:
		return 0, false
	}
}

func (p PartDeltaKind) Valid() bool {
	_, valid := p.allowedPayloads()
	return valid
}

// PartDelta is one transport increment. It is intentionally distinct from
// Part because incomplete tool arguments and citation attachment are not valid
// stable message content.
type PartDelta struct {
	Kind           PartDeltaKind  `json:"kind"`
	Text           string         `json:"text,omitempty"`
	Media          *media.Media   `json:"media,omitzero"`
	ReasoningState []byte         `json:"reasoning_state,omitempty"`
	ToolCall       *ToolCallDelta `json:"tool_call,omitzero"`
	Citation       *Citation      `json:"citation,omitzero"`
	Metadata       metadata.Map   `json:"metadata,omitzero"`
}

// NewTextDelta carries one non-empty text increment.
func NewTextDelta(text string) PartDelta {
	return PartDelta{Kind: PartDeltaText, Text: text}
}

func NewMediaDelta(value *media.Media) PartDelta {
	return PartDelta{Kind: PartDeltaMedia, Media: value}
}

// NewReasoningDelta snapshots visible reasoning or opaque replay state.
func NewReasoningDelta(text string, state []byte) PartDelta {
	return PartDelta{Kind: PartDeltaReasoning, Text: text, ReasoningState: slices.Clone(state)}
}

// NewToolCallDelta carries the next identity, name, or argument fragment for a
// tool call under construction.
func NewToolCallDelta(delta ToolCallDelta) PartDelta {
	return PartDelta{Kind: PartDeltaToolCall, ToolCall: new(delta)}
}

func NewCitationDelta(citation Citation) PartDelta {
	return PartDelta{Kind: PartDeltaCitation, Citation: new(citation)}
}

// NewRefusalDelta keeps refusal text distinct from ordinary text while it is
// streamed.
func NewRefusalDelta(text string) PartDelta {
	return PartDelta{Kind: PartDeltaRefusal, Text: text}
}

func (p PartDelta) Clone() PartDelta {
	clone := p
	clone.Media = p.Media.Clone()
	clone.ReasoningState = slices.Clone(p.ReasoningState)
	clone.Metadata = p.Metadata.Clone()
	if p.ToolCall != nil {
		clone.ToolCall = new(*p.ToolCall)
	}
	if p.Citation != nil {
		citation := *p.Citation
		clone.Citation = &citation
	}
	return clone
}

func (p PartDelta) carriedPayloads() deltaPayload {
	var carried deltaPayload
	if p.Text != "" {
		carried |= deltaPayloadText
	}
	if p.Media != nil {
		carried |= deltaPayloadMedia
	}
	if len(p.ReasoningState) != 0 {
		carried |= deltaPayloadReasoningState
	}
	if p.ToolCall != nil {
		carried |= deltaPayloadToolCall
	}
	if p.Citation != nil {
		carried |= deltaPayloadCitation
	}
	return carried
}

func (p PartDelta) Validate() error {
	allowed, valid := p.Kind.allowedPayloads()
	if !valid {
		return fmt.Errorf("%w: delta has unknown part kind %q", ErrInvalidResponse, p.Kind)
	}
	if err := p.Metadata.Validate(); err != nil {
		return fmt.Errorf("%w: delta metadata: %w", ErrInvalidResponse, err)
	}
	carried := p.carriedPayloads()
	if carried == 0 || carried&^allowed != 0 {
		return fmt.Errorf("%w: %s delta carries no payload or one its kind does not allow", ErrInvalidResponse, p.Kind)
	}
	return p.validateCarriedPayload()
}

func (p PartDelta) validateCarriedPayload() error {
	switch {
	case p.Media != nil:
		if err := p.Media.Validate(); err != nil {
			return fmt.Errorf("%w: media delta: %w", ErrInvalidResponse, err)
		}
	case p.ToolCall != nil:
		if err := p.ToolCall.Validate(); err != nil {
			return fmt.Errorf("%w: %w", ErrInvalidResponse, err)
		}
	case p.Citation != nil:
		if err := p.Citation.Validate(); err != nil {
			return fmt.Errorf("%w: %w", ErrInvalidResponse, err)
		}
	}
	return nil
}

func (p PartDelta) MarshalJSON() ([]byte, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	type wirePartDelta PartDelta
	return jsonv2.Marshal(wirePartDelta(p), jsonv2.Deterministic(true))
}

func (p *PartDelta) UnmarshalJSON(data []byte) error {
	if p == nil {
		return fmt.Errorf("%w: part delta receiver is nil", ErrInvalidResponse)
	}
	type wirePartDelta PartDelta
	var decoded wirePartDelta
	if err := jsonv2.Unmarshal(data, &decoded, jsonv2.RejectUnknownMembers(true)); err != nil {
		return fmt.Errorf("%w: decode part delta: %w", ErrInvalidResponse, err)
	}
	candidate := PartDelta(decoded)
	if err := candidate.Validate(); err != nil {
		return err
	}
	*p = candidate
	return nil
}

// ResponseDelta is one independently owned stream increment. Usage is a
// cumulative snapshot; an optional finish reason marks the terminal increment.
type ResponseDelta struct {
	Parts           []PartDelta       `json:"parts,omitempty"`
	MessageMetadata metadata.Map      `json:"message_metadata,omitzero"`
	FinishReason    FinishReason      `json:"finish_reason,omitempty"`
	OutputMetadata  *OutputMetadata   `json:"output_metadata,omitzero"`
	Metadata        *ResponseMetadata `json:"metadata,omitzero"`
}

func (r *ResponseDelta) Clone() *ResponseDelta {
	if r == nil {
		return nil
	}
	clone := &ResponseDelta{
		Parts:           make([]PartDelta, len(r.Parts)),
		MessageMetadata: r.MessageMetadata.Clone(),
		FinishReason:    r.FinishReason,
	}
	for index := range r.Parts {
		clone.Parts[index] = r.Parts[index].Clone()
	}
	if r.OutputMetadata != nil {
		clone.OutputMetadata = r.OutputMetadata.clone()
	}
	if r.Metadata != nil {
		clone.Metadata = r.Metadata.clone()
	}
	return clone
}

func (r *ResponseDelta) Text() string {
	if r == nil {
		return ""
	}
	var text strings.Builder
	for index := range r.Parts {
		if r.Parts[index].Kind == PartDeltaText {
			text.WriteString(r.Parts[index].Text)
		}
	}
	return text.String()
}

func (r *ResponseDelta) Validate() error {
	if r == nil {
		return fmt.Errorf("%w: nil response delta", ErrInvalidResponse)
	}
	if len(r.Parts) == 0 && len(r.MessageMetadata) == 0 && r.FinishReason == "" && r.OutputMetadata == nil && r.Metadata == nil {
		return fmt.Errorf("%w: empty response delta", ErrInvalidResponse)
	}
	for index := range r.Parts {
		if err := r.Parts[index].Validate(); err != nil {
			return fmt.Errorf("%w: parts[%d]: %w", ErrInvalidResponse, index, err)
		}
	}
	if err := r.MessageMetadata.Validate(); err != nil {
		return fmt.Errorf("%w: message metadata: %w", ErrInvalidResponse, err)
	}
	if r.FinishReason != "" && !r.FinishReason.Valid() {
		return fmt.Errorf("%w: unknown finish reason %q", ErrInvalidResponse, r.FinishReason)
	}
	if err := r.OutputMetadata.validate(); err != nil {
		return err
	}
	if err := r.Metadata.validate(); err != nil {
		return fmt.Errorf("%w: metadata: %w", ErrInvalidResponse, err)
	}
	return nil
}

func (r ResponseDelta) MarshalJSON() ([]byte, error) {
	if err := (&r).Validate(); err != nil {
		return nil, err
	}
	type wireResponseDelta ResponseDelta
	return jsonv2.Marshal(wireResponseDelta(r), jsonv2.Deterministic(true))
}

func (r *ResponseDelta) UnmarshalJSON(data []byte) error {
	if r == nil {
		return fmt.Errorf("%w: response delta receiver is nil", ErrInvalidResponse)
	}
	type wireResponseDelta ResponseDelta
	var decoded wireResponseDelta
	if err := jsonv2.Unmarshal(data, &decoded, jsonv2.RejectUnknownMembers(true)); err != nil {
		return fmt.Errorf("%w: decode response delta: %w", ErrInvalidResponse, err)
	}
	candidate := ResponseDelta(decoded)
	if err := candidate.Validate(); err != nil {
		return err
	}
	*r = candidate
	return nil
}
