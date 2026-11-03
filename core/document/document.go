package document

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"maps"
	"unicode/utf8"

	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/core/metadata"
)

var ErrInvalidDocument = errors.New("document: invalid document")

// Document is the canonical content carrier. Clone recursively snapshots media
// and metadata so indexing and retrieval boundaries never retain caller-owned
// mutable buffers.
type Document struct {
	ID string `json:"id,omitempty"`

	Text string `json:"text,omitempty"`

	Media *media.Media `json:"media,omitzero"`

	Metadata metadata.Map `json:"metadata,omitzero"`

	Provenance *Provenance `json:"provenance,omitzero"`
}

func (d *Document) Clone() *Document {
	if d == nil {
		return nil
	}
	clone := *d
	clone.Media = d.Media.Clone()
	clone.Metadata = d.Metadata.Clone()
	clone.Provenance = d.Provenance.Clone()
	return &clone
}

func (d Document) MarshalJSON() ([]byte, error) {
	if err := (&d).Validate(); err != nil {
		return nil, err
	}
	return jsonv2.Marshal(wireDocument(d))
}

func (d *Document) UnmarshalJSON(data []byte) error {
	if d == nil {
		return fmt.Errorf("%w: nil receiver", ErrInvalidDocument)
	}
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

// NewDocument requires text or media; their agreement remains the caller's
// responsibility.
func NewDocument(text string, payload *media.Media) (*Document, error) {
	doc := &Document{
		Text:     text,
		Media:    payload,
		Metadata: metadata.Map{},
	}
	if err := doc.Validate(); err != nil {
		return nil, fmt.Errorf("document: create: %w", err)
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
	if !utf8.ValidString(d.ID) {
		return fmt.Errorf("%w: ID is not valid UTF-8", ErrInvalidDocument)
	}
	if !utf8.ValidString(d.Text) {
		return fmt.Errorf("%w: text is not valid UTF-8", ErrInvalidDocument)
	}
	if d.Media != nil {
		if err := d.Media.Validate(); err != nil {
			return fmt.Errorf("%w: media: %w", ErrInvalidDocument, err)
		}
	}
	if err := d.Provenance.Validate(); err != nil {
		return err
	}
	if err := d.Metadata.Validate(); err != nil {
		return fmt.Errorf("%w: metadata: %w", ErrInvalidDocument, err)
	}
	return nil
}

type wireDocument Document

func (Document) JSONSchemaModel() any { return wireDocument{} }

// MarshalStoredMetadata encodes the document's business facts and optional
// provenance in one native JSON cell. Native filter projections must still
// be built from d.Metadata, never by indexing this physical representation.
func (d *Document) MarshalStoredMetadata() ([]byte, error) {
	if d == nil {
		return nil, ErrInvalidDocument
	}
	if err := ValidateStoredMetadata(d.Metadata); err != nil {
		return nil, err
	}
	if d.Provenance == nil {
		return d.Metadata.MarshalJSON()
	}
	if err := d.Provenance.Validate(); err != nil {
		return nil, err
	}
	cell := maps.Clone(d.Metadata)
	if err := cell.Set(StoredProvenanceKey, storedOrigin{d.Provenance, d.Metadata == nil}); err != nil {
		return nil, err
	}
	return cell.MarshalJSON()
}

// UnmarshalStoredMetadata restores both fields without changing the document's
// identity or content. Invalid native data leaves the document untouched. The
// raw cell may separately be retained as the backend's conditional-write token.
func (d *Document) UnmarshalStoredMetadata(data []byte) error {
	if d == nil {
		return ErrInvalidDocument
	}
	var facts metadata.Map
	if err := facts.UnmarshalJSON(data); err != nil {
		return err
	}
	var origin storedOrigin
	if raw, present := facts[StoredProvenanceKey]; present {
		if err := jsonv2.Unmarshal(raw, &origin, jsonv2.RejectUnknownMembers(true)); err != nil {
			return fmt.Errorf("%w: invalid stored provenance: %w", ErrInvalidDocument, err)
		}
		if origin.Provenance == nil {
			return fmt.Errorf("%w: absent stored provenance", ErrInvalidDocument)
		}
		if err := origin.Provenance.Validate(); err != nil {
			return fmt.Errorf("%w: stored provenance: %w", ErrInvalidDocument, err)
		}
		delete(facts, StoredProvenanceKey)
		if origin.NullMetadata {
			if len(facts) > 0 {
				return fmt.Errorf("%w: fields contradict null business metadata", ErrInvalidDocument)
			}
			facts = nil
		}
	}
	d.Metadata, d.Provenance = facts, origin.Provenance
	return nil
}
