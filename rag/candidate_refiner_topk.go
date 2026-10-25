package rag

import (
	"context"
	"errors"
)

var _ Refiner = (*TopKRefiner)(nil)

// TopKRefiner retains the bound admitted by TopK. Its zero value is invalid.
type TopKRefiner struct {
	topK int
}

// TopK returns a [Refiner] that stably sorts candidates by descending score and
// returns at most topK candidates. topK must be positive. Use [Dedup] before
// TopK when unique document identities are required.
func TopK(topK int) (*TopKRefiner, error) {
	if topK < 1 {
		return nil, errors.New("rag: top K must be positive")
	}
	return &TopKRefiner{topK: topK}, nil
}

func (t *TopKRefiner) Refine(ctx context.Context, query Query, candidates Candidates) (Candidates, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if t == nil || t.topK < 1 {
		return nil, errors.New("rag: top K must be positive")
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
