package rag

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"math"
	"slices"

	"github.com/Tangerg/scope/core/document"
)

var (
	// ErrInvalidCandidate identifies an invalid document or non-finite score.
	ErrInvalidCandidate = errors.New("rag: invalid retrieval candidate")
	// ErrNilTransformer rejects a missing query transformation stage.
	ErrNilTransformer = errors.New("rag: transformer must not be nil")
	// ErrNilExpander rejects a missing query expansion stage.
	ErrNilExpander = errors.New("rag: expander must not be nil")
	// ErrNilRefiner rejects a missing candidate refinement stage.
	ErrNilRefiner = errors.New("rag: refiner must not be nil")
	// ErrEmptyExpansion prevents an expander from erasing a query.
	ErrEmptyExpansion = errors.New("rag: expander returned no queries")
	// ErrInvalidExpansion identifies invalid or duplicate expanded queries.
	ErrInvalidExpansion = errors.New("rag: invalid query expansion")
)

// Candidate relates a document to the retrieval operation that produced it.
// Score is query-specific and therefore does not belong on document.Document.
type Candidate struct {
	Document *document.Document `json:"document"`
	Score    Score              `json:"score"`
}

// Score is a finite, query-relative ordering value. Scores are comparable only
// within the retrieval or fusion result that produced them.
type Score float64

func (s Score) Float64() float64 { return float64(s) }

func (s Score) Validate() error {
	if math.IsNaN(float64(s)) || math.IsInf(float64(s), 0) {
		return fmt.Errorf("%w: score must be finite", ErrInvalidCandidate)
	}
	return nil
}

// Clone returns an independently owned candidate and document.
func (c Candidate) Clone() Candidate {
	c.Document = c.Document.Clone()
	return c
}

// Candidates is an ordered retrieval result. Its methods never mutate the
// receiver, preserving declaration and retrieval order where scores tie.
type Candidates []Candidate

// Clone returns an independently owned candidate sequence.
func (c Candidates) Clone() Candidates {
	if c == nil {
		return nil
	}
	clone := make(Candidates, len(c))
	for index, candidate := range c {
		clone[index] = candidate.Clone()
	}
	return clone
}

func (c Candidates) Validate() error {
	for index, candidate := range c {
		if err := candidate.Validate(); err != nil {
			return fmt.Errorf("rag: candidate %d: %w", index, err)
		}
	}
	return nil
}

// uniqueBest returns the highest-scoring candidate for each known document
// identity. Identity-free documents remain distinct.
func (c Candidates) uniqueBest() Candidates {
	positions := make(map[string]int, len(c))
	unique := make(Candidates, 0, len(c))
	for _, candidate := range c {
		id := candidate.Document.ID
		if id == "" {
			unique = append(unique, candidate.Clone())
			continue
		}
		position, exists := positions[id]
		if !exists {
			positions[id] = len(unique)
			unique = append(unique, candidate.Clone())
			continue
		}
		if candidate.Score > unique[position].Score {
			unique[position] = candidate.Clone()
		}
	}
	return unique
}

// sortCandidatesByScore sorts an owned result. Refiners detach borrowed
// documents before handing them to this ordering step.
func sortCandidatesByScore(candidates Candidates) {
	slices.SortStableFunc(candidates, func(left, right Candidate) int {
		return cmp.Compare(right.Score, left.Score)
	})
}

func (c Candidate) Validate() error {
	if c.Document == nil {
		return fmt.Errorf("%w: document must not be nil", ErrInvalidCandidate)
	}
	if err := c.Document.Validate(); err != nil {
		return fmt.Errorf("%w: document: %w", ErrInvalidCandidate, err)
	}
	if err := c.Score.Validate(); err != nil {
		return err
	}
	return nil
}

// Transformer rewrites a query to be more retrieval-friendly — translation,
// compression, ambiguity resolution, vocabulary normalization.
type Transformer interface {
	// Transform returns one valid query that preserves the caller's retrieval
	// intent while changing its representation. It must not mutate query, must
	// honor ctx, and transfers ownership of the returned value to the caller.
	Transform(ctx context.Context, query Query) (Query, error)
}

// TransformerFunc adapts a function to Transformer.
type TransformerFunc func(context.Context, Query) (Query, error)

func (t TransformerFunc) Transform(ctx context.Context, query Query) (Query, error) {
	return t(ctx, query)
}

// Expander turns one query into many — useful for poorly formed inputs
// (alternative phrasings) or complex problems (decompose into sub-queries).
type Expander interface {
	// Expand returns a non-empty, ordered set of valid alternative or decomposed
	// queries with distinct Text values. Per-query values do not create another
	// expansion identity. It must not mutate query or expose reusable backing storage;
	// ordering is semantic because downstream fusion uses it for stable ties.
	Expand(ctx context.Context, query Query) ([]Query, error)
}

// ExpanderFunc adapts a function to Expander.
type ExpanderFunc func(context.Context, Query) ([]Query, error)

func (e ExpanderFunc) Expand(ctx context.Context, query Query) ([]Query, error) {
	return e(ctx, query)
}

// Retriever pulls candidate documents from a knowledge source.
type Retriever interface {
	// Retrieve returns independently owned, valid candidates in the source's
	// relevance order. Scores remain query-relative; the implementation must
	// honor ctx and must not mutate query.
	Retrieve(ctx context.Context, query Query) (Candidates, error)
}

// RetrieverFunc adapts a function to Retriever. Like the other stage function
// adapters, it forwards directly; the function must satisfy the stage contract.
// Combinators validate results received from external stages.
type RetrieverFunc func(context.Context, Query) (Candidates, error)

func (r RetrieverFunc) Retrieve(ctx context.Context, query Query) (Candidates, error) {
	return r(ctx, query)
}

// Refiner narrows candidate documents down to what the LLM should see.
type Refiner interface {
	// Refine returns a valid, independently owned subset or reordering of
	// candidates for query. It must not mutate either input; a successful empty
	// result explicitly means no evidence survived refinement.
	Refine(ctx context.Context, query Query, candidates Candidates) (Candidates, error)
}

// RefinerFunc adapts a function to Refiner.
type RefinerFunc func(context.Context, Query, Candidates) (Candidates, error)

func (r RefinerFunc) Refine(ctx context.Context, query Query, candidates Candidates) (Candidates, error) {
	return r(ctx, query, candidates)
}
