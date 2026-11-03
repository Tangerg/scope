package rag

import (
	"context"
)

var _ Refiner = Deduplicator{}

// Deduplicator is the [Refiner] that keeps the highest-scoring candidate for
// each nonempty document ID within the same source revision and location. Its
// zero value is ready to use. For unversioned local data, the caller owns the
// ID namespace. Identities retain first-seen order, and equal scores retain
// the first candidate. Documents without an ID remain distinct because the
// framework cannot prove they are duplicates.
type Deduplicator struct{}

func (d Deduplicator) Refine(ctx context.Context, query Query, candidates Candidates) (Candidates, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := query.Validate(); err != nil {
		return nil, err
	}
	if err := candidates.Validate(); err != nil {
		return nil, err
	}
	return candidates.uniqueBest()
}
