package document

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/core/metadata"
)

var ErrInvalidDocument = errors.New("document: invalid document")

// Document is the canonical content carrier. It holds identity, content, and
// metadata; query-specific relationships and runtime policies live outside
// this value. Clone recursively snapshots media and metadata so indexing and
// retrieval boundaries never retain caller-owned mutable buffers.
type Document struct {
	ID string `json:"id,omitempty"`

	// Text is the textual content. May be empty if Media is set.
	Text string `json:"text,omitempty"`

	Media *media.Media `json:"media,omitempty"`

	Metadata metadata.Map `json:"metadata,omitzero"`
}

func (d *Document) Clone() *Document {
	if d == nil {
		return nil
	}
	clone := *d
	clone.Media = d.Media.Clone()
	clone.Metadata = d.Metadata.Clone()
	return &clone
}

func (d Document) MarshalJSON() ([]byte, error) {
	if err := (&d).Validate(); err != nil {
		return nil, err
	}
	type wireDocument Document
	return json.Marshal(wireDocument(d))
}

func (d *Document) UnmarshalJSON(data []byte) error {
	if d == nil {
		return fmt.Errorf("%w: nil receiver", ErrInvalidDocument)
	}
	type wireDocument Document
	var decoded wireDocument
	if err := jsonv2.Unmarshal(data, &decoded, jsonv2.RejectUnknownMembers(true)); err != nil {
		return fmt.Errorf("%w: decode: %w", ErrInvalidDocument, err)
	}
	candidate := Document(decoded)
	if err := candidate.Validate(); err != nil {
		return err
	}
	*d = candidate
	return nil
}

// NewDocument requires at least one of text or media. A document with neither
// carries nothing to rank or read, and would travel through splitting,
// embedding, and retrieval before failing somewhere that no longer identifies
// where it was created. Whether the two describe the same thing is the caller's
// responsibility; only their presence is checked here.
func NewDocument(text string, payload *media.Media) (*Document, error) {
	if text == "" && payload == nil {
		return nil, fmt.Errorf("document: create: %w: text or media is required", ErrInvalidDocument)
	}

	doc := &Document{
		Text:     text,
		Media:    payload,
		Metadata: metadata.Map{},
	}
	if err := doc.Validate(); err != nil {
		return nil, err
	}
	return doc, nil
}

func (d *Document) Validate() error {
	if d == nil {
		return fmt.Errorf("%w: receiver is nil", ErrInvalidDocument)
	}
	if d.Text == "" && d.Media == nil {
		return fmt.Errorf("%w: text or media is required", ErrInvalidDocument)
	}
	if !utf8.ValidString(d.Text) {
		return fmt.Errorf("%w: text is not valid UTF-8", ErrInvalidDocument)
	}
	if d.Media != nil {
		if err := d.Media.Validate(); err != nil {
			return fmt.Errorf("%w: media: %w", ErrInvalidDocument, err)
		}
	}
	if err := d.Metadata.Validate(); err != nil {
		return fmt.Errorf("%w: metadata: %w", ErrInvalidDocument, err)
	}
	return nil
}
