package chat

import (
	"encoding/json"
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

func (p PartDeltaKind) Valid() bool {
	switch p {
	case PartDeltaText, PartDeltaMedia, PartDeltaReasoning, PartDeltaToolCall, PartDeltaCitation, PartDeltaRefusal:
		return true
	default:
		return false
	}
}

// PartDelta is one transport increment. It is intentionally distinct from
// Part because incomplete tool arguments and citation attachment are not valid
// stable message content.
type PartDelta struct {
	Kind           PartDeltaKind  `json:"kind"`
	Text           string         `json:"text,omitempty"`
	Media          *media.Media   `json:"media,omitempty"`
	ReasoningState []byte         `json:"reasoning_state,omitempty"`
	ToolCall       *ToolCallDelta `json:"tool_call,omitempty"`
	Citation       *Citation      `json:"citation,omitempty"`
	Metadata       metadata.Map   `json:"metadata,omitzero"`
}

// NewTextDelta carries one non-empty text increment.
func NewTextDelta(text string) PartDelta {
	return PartDelta{Kind: PartDeltaText, Text: text}
}

// NewMediaDelta carries one complete media value delivered by the stream.
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

// NewCitationDelta attaches one complete evidence item to streamed text.
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

func (p PartDelta) Validate() error {
	if !p.Kind.Valid() {
		return fmt.Errorf("%w: delta has unknown part kind %q", ErrInvalidResponse, p.Kind)
	}
	if err := p.Metadata.Validate(); err != nil {
		return fmt.Errorf("%w: delta metadata: %w", ErrInvalidResponse, err)
	}
	switch p.Kind {
	case PartDeltaText, PartDeltaRefusal:
		if p.Text == "" || p.Media != nil || len(p.ReasoningState) != 0 || p.ToolCall != nil || p.Citation != nil {
			return fmt.Errorf("%w: %s delta requires non-empty text and no other payload", ErrInvalidResponse, p.Kind)
		}
	case PartDeltaMedia:
		if p.Text != "" || p.Media == nil || len(p.ReasoningState) != 0 || p.ToolCall != nil || p.Citation != nil {
			return fmt.Errorf("%w: media delta requires its matching payload", ErrInvalidResponse)
		}
		if err := p.Media.Validate(); err != nil {
			return fmt.Errorf("%w: media delta: %w", ErrInvalidResponse, err)
		}
	case PartDeltaReasoning:
		if p.Text == "" && len(p.ReasoningState) == 0 || p.Media != nil || p.ToolCall != nil || p.Citation != nil {
			return fmt.Errorf("%w: reasoning delta requires text or state and no other payload", ErrInvalidResponse)
		}
	case PartDeltaToolCall:
		if p.Text != "" || p.Media != nil || len(p.ReasoningState) != 0 || p.ToolCall == nil || p.Citation != nil {
			return fmt.Errorf("%w: tool call delta requires its matching payload", ErrInvalidResponse)
		}
		if err := p.ToolCall.Validate(); err != nil {
			return fmt.Errorf("%w: %w", ErrInvalidResponse, err)
		}
	case PartDeltaCitation:
		if p.Text != "" || p.Media != nil || len(p.ReasoningState) != 0 || p.ToolCall != nil || p.Citation == nil {
			return fmt.Errorf("%w: citation delta requires its matching payload", ErrInvalidResponse)
		}
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
	return json.Marshal(wirePartDelta(p))
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
	OutputMetadata  *OutputMetadata   `json:"output_metadata,omitempty"`
	Metadata        *ResponseMetadata `json:"metadata,omitempty"`
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
	return json.Marshal(wireResponseDelta(r))
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
