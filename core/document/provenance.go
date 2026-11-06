package document

import (
	"crypto/sha256"
	"encoding/hex"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"slices"
	"unicode/utf8"
)

// ErrUnversionedSource distinguishes local material from a reproducible source.
var ErrUnversionedSource = errors.New("document: source revision is unknown")

// Provenance describes where a document came from, independently of its store
// ID and searchable business metadata. Source and Revision identify one original;
// a relative chunk path can still be known for an anonymous in-memory input.
type Provenance struct {
	Source   string     `json:"source,omitempty"`
	Revision string     `json:"revision,omitempty"`
	Chunks   []Chunk    `json:"chunks,omitempty"`
	Bytes    *ByteRange `json:"bytes,omitzero"`
	Page     int        `json:"page,omitempty"` // one-based; zero means unknown
}

// Chunk identifies an emitted part within its parent, after empty parts have
// been removed. Nested splitting appends a step instead of overwriting history.
type Chunk struct {
	Parent string `json:"parent,omitempty"`
	Index  int    `json:"index"`
	Total  int    `json:"total"`
}

func (c Chunk) Validate() error {
	if !utf8.ValidString(c.Parent) || c.Index < 0 || c.Index >= c.Total {
		return fmt.Errorf("%w: invalid chunk position", ErrInvalidDocument)
	}
	return nil
}

func (c Chunk) MarshalJSON() ([]byte, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return jsonv2.Marshal(wireChunk(c))
}

func (c *Chunk) UnmarshalJSON(data []byte) error {
	if c == nil {
		return fmt.Errorf("%w: nil chunk receiver", ErrInvalidDocument)
	}
	var decoded struct {
		Parent string `json:"parent,omitempty"`
		Index  *int   `json:"index"`
		Total  *int   `json:"total"`
	}
	if err := jsonv2.Unmarshal(data, &decoded, jsonv2.RejectUnknownMembers(true)); err != nil {
		return fmt.Errorf("%w: chunk: %w", ErrInvalidDocument, err)
	}
	if decoded.Index == nil || decoded.Total == nil {
		return fmt.Errorf("%w: chunk requires index and total", ErrInvalidDocument)
	}
	candidate := Chunk{Parent: decoded.Parent, Index: *decoded.Index, Total: *decoded.Total}
	if err := candidate.Validate(); err != nil {
		return err
	}
	*c = candidate
	return nil
}

type wireChunk Chunk

func (Chunk) JSONSchemaModel() any { return wireChunk{} }

// ByteRange is a half-open interval in the original source bytes. A nil range
// means that a transform could not prove an interval in the original.
type ByteRange struct {
	Start int64 `json:"start"`
	End   int64 `json:"end"`
}

func (b ByteRange) Validate() error {
	if b.Start < 0 || b.End < b.Start {
		return fmt.Errorf("%w: invalid original byte range", ErrInvalidDocument)
	}
	return nil
}

func (b ByteRange) MarshalJSON() ([]byte, error) {
	if err := b.Validate(); err != nil {
		return nil, err
	}
	return jsonv2.Marshal(wireByteRange(b))
}

func (b *ByteRange) UnmarshalJSON(data []byte) error {
	if b == nil {
		return fmt.Errorf("%w: nil byte range receiver", ErrInvalidDocument)
	}
	var decoded struct {
		Start *int64 `json:"start"`
		End   *int64 `json:"end"`
	}
	if err := jsonv2.Unmarshal(data, &decoded, jsonv2.RejectUnknownMembers(true)); err != nil {
		return fmt.Errorf("%w: byte range: %w", ErrInvalidDocument, err)
	}
	if decoded.Start == nil || decoded.End == nil {
		return fmt.Errorf("%w: byte range requires start and end", ErrInvalidDocument)
	}
	candidate := ByteRange{Start: *decoded.Start, End: *decoded.End}
	if err := candidate.Validate(); err != nil {
		return err
	}
	*b = candidate
	return nil
}

type wireByteRange ByteRange

func (ByteRange) JSONSchemaModel() any { return wireByteRange{} }

// NewProvenance binds a named source to the digest of its original bytes.
// Reading the same name after an edit therefore creates a different revision.
func NewProvenance(source string, data []byte) *Provenance {
	if source == "" {
		return nil
	}
	sum := sha256.Sum256(data)
	return &Provenance{Source: source, Revision: "sha256:" + hex.EncodeToString(sum[:])}
}

func (p *Provenance) Clone() *Provenance {
	if p == nil {
		return nil
	}
	clone := *p
	clone.Chunks = slices.Clone(p.Chunks)
	if p.Bytes != nil {
		b := *p.Bytes
		clone.Bytes = &b
	}
	return &clone
}

// Excerpt preserves only locations that apply to the entire submitted input.
// A remote segmenter or text transform cannot establish its original byte range.
func (p *Provenance) Excerpt() *Provenance {
	if p == nil || (p.Source == "" && len(p.Chunks) == 0 && p.Page == 0) {
		return nil
	}
	clone := *p
	clone.Bytes = nil
	clone.Chunks = slices.Clone(p.Chunks)
	return &clone
}

// Split adds a proven, relative position to the surviving source evidence.
func (p *Provenance) Split(parent string, index, total int) *Provenance {
	chunk := p.Excerpt()
	if chunk == nil {
		chunk = &Provenance{}
	}
	chunk.Chunks = append(chunk.Chunks, Chunk{Parent: parent, Index: index, Total: total})
	return chunk
}

func (p *Provenance) Validate() error {
	if p == nil {
		return nil
	}
	if err := SourceName(p.Source).Validate(); err != nil {
		return err
	}
	if (p.Source == "") != (p.Revision == "") || !validProvenanceText(p.Revision) {
		return fmt.Errorf("%w: provenance requires a source and its revision together", ErrInvalidDocument)
	}
	if p.Source == "" && len(p.Chunks) == 0 && p.Page == 0 && p.Bytes == nil {
		return fmt.Errorf("%w: provenance contains no source or location", ErrInvalidDocument)
	}
	if p.Page < 0 {
		return fmt.Errorf("%w: invalid original page", ErrInvalidDocument)
	}
	if p.Bytes != nil {
		if err := p.Bytes.Validate(); err != nil {
			return err
		}
	}
	for _, c := range p.Chunks {
		if err := c.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// RequireRevision is the admission boundary for reproducible ingestion and
// source citations. Relative positions alone cannot identify an original.
func (p *Provenance) RequireRevision() error {
	if err := p.Validate(); err != nil {
		return err
	}
	if p == nil || p.Source == "" {
		return ErrUnversionedSource
	}
	return nil
}
