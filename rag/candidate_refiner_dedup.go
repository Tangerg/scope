package rag

import (
	"context"
)

var _ Refiner = deduper{}

type deduper struct{}

// Dedup returns a [Refiner] that keeps the highest-scoring candidate for each
// non-empty [document.Document.ID]. Document identities retain first-seen
// order, and equal scores retain the first candidate. Documents without an ID
// remain distinct because the framework cannot prove they are duplicates.
func Dedup() Refiner {
	return deduper{}
}

func (d deduper) Refine(ctx context.Context, query Query, candidates Candidates) (Candidates, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := query.Validate(); err != nil {
		return nil, err
	}
	if err := candidates.Validate(); err != nil {
		return nil, err
	}
	return candidates.uniqueBest(), nil
}
