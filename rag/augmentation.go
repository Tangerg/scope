package rag

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

var (
	// ErrInvalidAugmentation identifies generation input or citation numbering
	// that cannot be represented portably.
	ErrInvalidAugmentation = errors.New("rag: invalid augmentation")
	// ErrNilAugmenter rejects composition without explicit prompt policy.
	ErrNilAugmenter = errors.New("rag: augmenter must not be nil")
)

// Augmentation is immutable generation input that can be copied by assignment.
// It keeps final text and citations separate from [Query]'s retrieval-scoped
// values.
type Augmentation struct {
	text      string
	citations Citations
}

// Citation binds a one-based prompt marker to the candidate it identifies.
type Citation struct {
	Number    int       `json:"number"`
	Candidate Candidate `json:"candidate"`
}

// Citations is an ordered, one-based mapping between prompt markers and
// retrieval candidates.
type Citations []Citation

// Clone returns an independently owned citation sequence.
func (c Citations) Clone() Citations {
	if c == nil {
		return nil
	}
	clone := make(Citations, len(c))
	for index, citation := range c {
		clone[index] = citation.Clone()
	}
	return clone
}

func (c Citations) Validate() error {
	for index, citation := range c {
		if err := citation.Validate(); err != nil {
			return err
		}
		if citation.Number != index+1 {
			return fmt.Errorf(
				"%w: citation number %d at position %d, want %d",
				ErrInvalidAugmentation,
				citation.Number,
				index,
				index+1,
			)
		}
	}
	return nil
}

// NewCitation snapshots a candidate under one positive prompt marker.
func NewCitation(number int, candidate Candidate) (Citation, error) {
	citation := Citation{Number: number, Candidate: candidate.Clone()}
	if err := citation.Validate(); err != nil {
		return Citation{}, err
	}
	return citation, nil
}

// Clone returns an independently owned citation and candidate.
func (c Citation) Clone() Citation {
	c.Candidate = c.Candidate.Clone()
	return c
}

// Marker returns the stable prompt marker for this citation.
func (c Citation) Marker() string { return fmt.Sprintf("[%d]", c.Number) }

func (c Citation) Validate() error {
	if c.Number <= 0 {
		return fmt.Errorf("%w: citation number must be positive", ErrInvalidAugmentation)
	}
	if err := c.Candidate.Validate(); err != nil {
		return fmt.Errorf("%w: citation %d: %w", ErrInvalidAugmentation, c.Number, err)
	}
	return nil
}

// NewAugmentation validates final generation text before citations are attached.
func NewAugmentation(text string) (Augmentation, error) {
	augmentation := Augmentation{text: text}
	if err := augmentation.Validate(); err != nil {
		return Augmentation{}, err
	}
	return augmentation, nil
}

// Text returns the final generation input.
func (a Augmentation) Text() string { return a.text }

// Citations returns an independent citation-order snapshot.
func (a Augmentation) Citations() Citations { return a.citations.Clone() }

// WithCitations returns an independent augmentation with citations. Numbers
// must be consecutive and one-based so prompt markers have one interpretation.
func (a Augmentation) WithCitations(citations Citations) (Augmentation, error) {
	a.citations = citations.Clone()
	if err := a.Validate(); err != nil {
		return Augmentation{}, err
	}
	return a, nil
}

func (a Augmentation) Validate() error {
	if strings.TrimSpace(a.text) == "" {
		return fmt.Errorf("%w: text must not be blank", ErrInvalidAugmentation)
	}
	return a.citations.Validate()
}

// Augmenter turns a retrieval query and its candidates into final generation
// input.
type Augmenter interface {
	// Augment creates the complete generation input from one query and its
	// ordered candidates. It must not mutate either input, must preserve any
	// citation-to-candidate relationship it emits, and must honor ctx.
	Augment(ctx context.Context, query Query, candidates Candidates) (Augmentation, error)
}

// AugmenterFunc adapts a function to Augmenter without another composition API.
type AugmenterFunc func(context.Context, Query, Candidates) (Augmentation, error)

func (a AugmenterFunc) Augment(ctx context.Context, query Query, candidates Candidates) (Augmentation, error) {
	return a(ctx, query, candidates)
}

func augment(ctx context.Context, augmenter Augmenter, query Query, candidates Candidates) (Augmentation, error) {
	if err := ctx.Err(); err != nil {
		return Augmentation{}, err
	}
	if err := query.Validate(); err != nil {
		return Augmentation{}, err
	}
	if err := candidates.Validate(); err != nil {
		return Augmentation{}, err
	}
	augmentation, err := augmenter.Augment(ctx, query, candidates)
	if err != nil {
		return Augmentation{}, err
	}
	if err := augmentation.Validate(); err != nil {
		return Augmentation{}, err
	}
	if err := ctx.Err(); err != nil {
		return Augmentation{}, err
	}
	return augmentation, nil
}
