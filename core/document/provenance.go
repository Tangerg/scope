package document

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
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

// ByteRange is a half-open interval in the original source bytes. A nil range
// means that a transform could not prove an interval in the original.
type ByteRange struct {
	Start int64 `json:"start"`
	End   int64 `json:"end"`
}

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
	if (p.Source == "") != (p.Revision == "") || !validSourceText(p.Source) || !validSourceText(p.Revision) {
		return fmt.Errorf("%w: provenance requires a source and its revision together", ErrInvalidDocument)
	}
	if p.Source == "" && len(p.Chunks) == 0 && p.Page == 0 && p.Bytes == nil {
		return fmt.Errorf("%w: provenance contains no source or location", ErrInvalidDocument)
	}
	if p.Page < 0 || (p.Bytes != nil && (p.Bytes.Start < 0 || p.Bytes.End < p.Bytes.Start)) {
		return fmt.Errorf("%w: invalid original page or byte range", ErrInvalidDocument)
	}
	for _, c := range p.Chunks {
		if !utf8.ValidString(c.Parent) || c.Index < 0 || c.Index >= c.Total {
			return fmt.Errorf("%w: invalid chunk position", ErrInvalidDocument)
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

func validSourceText(s string) bool {
	return s == "" || (utf8.ValidString(s) && strings.TrimSpace(s) != "")
}
