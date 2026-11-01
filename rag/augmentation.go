package rag

import (
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Tangerg/scope/core/document"
)

var (
	// ErrInvalidAugmentation identifies generation input or citation numbering
	// that cannot be represented portably.
	ErrInvalidAugmentation = errors.New("rag: invalid augmentation")
	ErrNilAugmenter        = errors.New("rag: augmenter must not be nil")
)

// Augmentation is immutable generation input that can be copied by assignment.
// It keeps final text and citations separate from [Query]'s retrieval-scoped
// values.
type Augmentation struct {
	text      string
	citations Citations
}

// Citations is the immutable selection supplied to one prompt. The position
// owns its marker; neither model output nor changes to a returned document can
// create or renumber an entry. Its zero value is an empty selection.
type Citations struct{ selected Candidates }

// Len reports how many prompt markers can resolve to admitted material.
func (c Citations) Len() int { return len(c.selected) }

// Candidates returns an owned snapshot of admitted evidence in marker order.
// This also exposes temporary material without claiming that it is reproducible.
func (c Citations) Candidates() Candidates { return c.selected.Clone() }

// ErrUnknownCitation means the marker is malformed or is outside this prompt's selection.
var ErrUnknownCitation = errors.New("rag: unknown citation marker")

// ErrUnidentifiedCitation means the selected document has no stable document ID.
var ErrUnidentifiedCitation = errors.New("rag: citation requires a document ID")

// Resolve returns the original, detached document for an exact canonical prompt
// marker such as "[1]". Its Provenance owns the source, revision and known
// location. Anonymous material returns document.ErrUnversionedSource. Resolution
// neither fetches a source nor establishes that it supports a generated claim.
func (c Citations) Resolve(marker string) (*document.Document, error) {
	if len(marker) < 3 || marker[0] != '[' || marker[len(marker)-1] != ']' {
		return nil, ErrUnknownCitation
	}
	n, err := strconv.Atoi(marker[1 : len(marker)-1])
	if err != nil || n < 1 || n > len(c.selected) || CitationMarker(n-1) != marker {
		return nil, ErrUnknownCitation
	}
	doc := c.selected[n-1].Document
	if err := doc.Provenance.RequireRevision(); err != nil {
		return nil, fmt.Errorf("rag: citation %s: %w", marker, err)
	}
	if strings.TrimSpace(doc.ID) == "" {
		return nil, fmt.Errorf("%w: %s", ErrUnidentifiedCitation, marker)
	}
	return doc.Clone(), nil
}

// MarshalJSON exposes the ordered admitted entries to host clients. Position,
// rather than a second stored field, determines each entry's marker.
func (c Citations) MarshalJSON() ([]byte, error) { return jsonv2.Marshal(c.selected) }

// CitationMarker is the one-based prompt marker of the citation at index.
func CitationMarker(index int) string { return fmt.Sprintf("[%d]", index+1) }

// NewAugmentation preserves non-blank UTF-8 generation text exactly.
func NewAugmentation(text string) (Augmentation, error) {
	augmentation := Augmentation{text: text}
	if err := augmentation.Validate(); err != nil {
		return Augmentation{}, err
	}
	return augmentation, nil
}

func (a Augmentation) Text() string { return a.text }

// Citations returns the immutable selection used for this prompt.
func (a Augmentation) Citations() Citations { return a.citations }

func (a Augmentation) Validate() error {
	if !utf8.ValidString(a.text) {
		return fmt.Errorf("%w: text must be valid UTF-8", ErrInvalidAugmentation)
	}
	if strings.TrimSpace(a.text) == "" {
		return fmt.Errorf("%w: text must not be blank", ErrInvalidAugmentation)
	}
	if err := a.citations.selected.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidAugmentation, err)
	}
	return nil
}

type Augmenter interface {
	// Augment creates the complete generation input from one query and its
	// ordered candidates. It must not mutate either input, must preserve any
	// citation-to-candidate relationship it emits, and must honor ctx.
	Augment(ctx context.Context, query Query, candidates Candidates) (Augmentation, error)
}

type AugmenterFunc func(context.Context, Query, Candidates) (Augmentation, error)

func (a AugmenterFunc) Augment(ctx context.Context, query Query, candidates Candidates) (Augmentation, error) {
	return a(ctx, query, candidates)
}
