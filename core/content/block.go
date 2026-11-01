package content

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"slices"
	"unicode/utf8"

	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/core/metadata"
)

var ErrInvalidBlock = errors.New("content: invalid block")

type Kind string

const (
	Text  = "text"
	Media = "media"
)

// Block carries content, never a control instruction, tool call, or nested result.
type Block struct {
	Kind      Kind         `json:"kind"`
	Text      string       `json:"text,omitempty"`
	Media     *media.Media `json:"media,omitzero"`
	Citations []Citation   `json:"citations,omitempty"`
	Metadata  metadata.Map `json:"metadata,omitzero"`
}

func (b Block) Validate() error {
	if err := b.Metadata.Validate(); err != nil {
		return fmt.Errorf("%w: metadata: %w", ErrInvalidBlock, err)
	}
	switch b.Kind {
	case Text:
		if b.Text == "" || !utf8.ValidString(b.Text) || b.Media != nil {
			return fmt.Errorf("%w: text must be non-empty UTF-8 without media", ErrInvalidBlock)
		}
		for i := range b.Citations {
			if err := b.Citations[i].Validate(); err != nil {
				return fmt.Errorf("%w: citations[%d]: %w", ErrInvalidBlock, i, err)
			}
		}
	case Media:
		if b.Text != "" || b.Media == nil || len(b.Citations) != 0 {
			return fmt.Errorf("%w: media cannot carry text or citations", ErrInvalidBlock)
		}
		if err := b.Media.Validate(); err != nil {
			return fmt.Errorf("%w: media: %w", ErrInvalidBlock, err)
		}
	default:
		return fmt.Errorf("%w: unsupported kind %q", ErrInvalidBlock, b.Kind)
	}
	return nil
}
func (b Block) Clone() Block {
	b.Media = b.Media.Clone()
	b.Citations = slices.Clone(b.Citations)
	b.Metadata = b.Metadata.Clone()
	return b
}

func (b Block) MarshalJSON() ([]byte, error) {
	if err := b.Validate(); err != nil {
		return nil, err
	}
	return jsonv2.Marshal(wireBlock(b), jsonv2.Deterministic(true))
}

func (b *Block) UnmarshalJSON(data []byte) error {
	if b == nil {
		return fmt.Errorf("%w: nil content receiver", ErrInvalidBlock)
	}
	var decoded wireBlock
	if err := jsonv2.Unmarshal(data, &decoded, jsonv2.RejectUnknownMembers(true)); err != nil {
		return fmt.Errorf("%w: decode content: %w", ErrInvalidBlock, err)
	}
	candidate := Block(decoded)
	if err := candidate.Validate(); err != nil {
		return err
	}
	*b = candidate
	return nil
}

type wireBlock Block

func (Block) JSONSchemaModel() any { return wireBlock{} }
