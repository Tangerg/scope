package rag

import (
	"context"
	"errors"
)

var _ Refiner = topKRefiner{}

// topKRefiner selects the highest-scoring candidates.
type topKRefiner struct {
	topK int
}

// TopK returns a [Refiner] that stably sorts candidates by descending score and
// returns at most topK candidates. topK must be positive. Use [Dedup] before
// TopK when unique document identities are required.
func TopK(topK int) (Refiner, error) {
	if topK < 1 {
		return nil, errors.New("rag: top K must be positive")
	}
	return topKRefiner{topK: topK}, nil
}

// Refine returns at most topK candidates ordered by descending score.
// The input slice is not mutated. Honors ctx cancellation.
func (t topKRefiner) Refine(ctx context.Context, query Query, candidates Candidates) (Candidates, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := query.Validate(); err != nil {
		return nil, err
	}
	if err := candidates.Validate(); err != nil {
		return nil, err
	}

	sorted := candidates.Clone()
	sorted.sortByScore()

	if len(sorted) > t.topK {
		sorted = sorted[:t.topK]
	}
	return sorted, nil
}
