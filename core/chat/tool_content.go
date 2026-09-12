package chat

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"fmt"
	"slices"

	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/core/metadata"
)

// ToolContent is one text or media value in a ToolOutput. Kind must be PartText
// or PartMedia. It shares media and citation contracts with Part without
// admitting chat control payloads or recursively nested tool results.
type ToolContent struct {
	Kind      PartKind     `json:"kind"`
	Text      string       `json:"text,omitempty"`
	Media     *media.Media `json:"media,omitempty"`
	Citations []Citation   `json:"citations,omitempty"`
	Metadata  metadata.Map `json:"metadata,omitzero"`
}

func (t ToolContent) Validate() error {
	if t.Kind != PartText && t.Kind != PartMedia {
		return fmt.Errorf("%w: unsupported content kind %q", ErrInvalidToolOutput, t.Kind)
	}
	return (Part{Kind: t.Kind, Text: t.Text, Media: t.Media, Citations: t.Citations, Metadata: t.Metadata}).Validate()
}

func (t ToolContent) Clone() ToolContent {
	t.Media = t.Media.Clone()
	t.Citations = slices.Clone(t.Citations)
	t.Metadata = t.Metadata.Clone()
	return t
}

func (t ToolContent) MarshalJSON() ([]byte, error) {
	if err := t.Validate(); err != nil {
		return nil, err
	}
	type wire ToolContent
	return json.Marshal(wire(t))
}

func (t *ToolContent) UnmarshalJSON(data []byte) error {
	if t == nil {
		return fmt.Errorf("%w: nil content receiver", ErrInvalidToolOutput)
	}
	type wire ToolContent
	var decoded wire
	if err := jsonv2.Unmarshal(data, &decoded, jsonv2.RejectUnknownMembers(true)); err != nil {
		return fmt.Errorf("%w: decode content: %w", ErrInvalidToolOutput, err)
	}
	candidate := ToolContent(decoded)
	if err := candidate.Validate(); err != nil {
		return err
	}
	*t = candidate
	return nil
}
