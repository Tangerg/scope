package rag

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
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

// Citations is the ordered evidence an Augmentation's prompt cites. A
// candidate's position owns its prompt marker: the candidate at index i is
// cited as CitationMarker(i), so no stored number can disagree with the order.
type Citations []Candidate

func (c Citations) Clone() Citations {
	return Citations(Candidates(c).Clone())
}

func (c Citations) Validate() error {
	for index, candidate := range c {
		if err := candidate.Validate(); err != nil {
			return fmt.Errorf("%w: citation %s: %w", ErrInvalidAugmentation, CitationMarker(index), err)
		}
	}
	return nil
}

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

// Citations returns an independent citation-order snapshot.
func (a Augmentation) Citations() Citations { return a.citations.Clone() }

// WithCitations returns an independent augmentation with citations, each cited
// by the marker of its position.
func (a Augmentation) WithCitations(citations Citations) (Augmentation, error) {
	a.citations = citations.Clone()
	if err := a.Validate(); err != nil {
		return Augmentation{}, err
	}
	return a, nil
}

func (a Augmentation) Validate() error {
	if !utf8.ValidString(a.text) {
		return fmt.Errorf("%w: text must be valid UTF-8", ErrInvalidAugmentation)
	}
	if strings.TrimSpace(a.text) == "" {
		return fmt.Errorf("%w: text must not be blank", ErrInvalidAugmentation)
	}
	return a.citations.Validate()
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
