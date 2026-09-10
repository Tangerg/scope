package rag

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/samber/lo"
)

// ErrInvalidRankConstant identifies a fusion policy that cannot preserve
// monotonic rank contribution.
var ErrInvalidRankConstant = errors.New("rag: reciprocal-rank constant must not be negative")

// DefaultReciprocalRankConstant is the conventional RRF smoothing constant.
const DefaultReciprocalRankConstant = 60

// DefaultMaxConcurrentRetrievals bounds fan-out when no limit is specified.
const DefaultMaxConcurrentRetrievals = 4

// ErrInvalidRetrievalConcurrency rejects a negative retrieval concurrency bound.
var ErrInvalidRetrievalConcurrency = errors.New("rag: retrieval concurrency must not be negative")

// ReciprocalRankFusionConfig controls ranking and per-call retrieval fan-out.
// RankConstant is added to each one-based rank before reciprocal weighting;
// zero uses [DefaultReciprocalRankConstant]. MaxConcurrentRetrievals bounds
// active child calls; zero uses [DefaultMaxConcurrentRetrievals], and one
// executes sequentially. The bound applies independently to each invocation.
type ReciprocalRankFusionConfig struct {
	RankConstant            int
	MaxConcurrentRetrievals int
}

func (r ReciprocalRankFusionConfig) normalized() (ReciprocalRankFusionConfig, error) {
	if r.RankConstant < 0 {
		return ReciprocalRankFusionConfig{}, ErrInvalidRankConstant
	}
	if r.MaxConcurrentRetrievals < 0 {
		return ReciprocalRankFusionConfig{}, ErrInvalidRetrievalConcurrency
	}
	if r.MaxConcurrentRetrievals == 0 {
		r.MaxConcurrentRetrievals = DefaultMaxConcurrentRetrievals
	}
	if r.RankConstant == 0 {
		r.RankConstant = DefaultReciprocalRankConstant
	}
	return r, nil
}

// ReciprocalRankFusion returns a retriever that concurrently executes each
// input retriever and fuses their ordered results using reciprocal-rank
// fusion. Raw candidate scores are deliberately ignored because independent
// retrievers commonly use incomparable score scales. Every retriever must
// succeed; failures are reported in declaration order without partial results.
func ReciprocalRankFusion(config ReciprocalRankFusionConfig, retrievers ...Retriever) (Retriever, error) {
	config, err := config.normalized()
	if err != nil {
		return nil, err
	}
	if len(retrievers) == 0 {
		return nil, ErrNilRetriever
	}
	owned := slices.Clone(retrievers)
	for index, retriever := range owned {
		if lo.IsNil(retriever) {
			return nil, fmt.Errorf("rag.ReciprocalRankFusion: retriever %d: %w", index, ErrNilRetriever)
		}
	}

	return reciprocalRankFusion{config: config, retrievers: owned}, nil
}

type reciprocalRankFusion struct {
	config     ReciprocalRankFusionConfig
	retrievers []Retriever
}

func (r reciprocalRankFusion) Retrieve(ctx context.Context, query Query) (candidates Candidates, err error) {
	if validateErr := query.Validate(); validateErr != nil {
		return nil, validateErr
	}
	rankings, err := parallelResults(ctx, "rag.ReciprocalRankFusion", r.retrievers, "retriever", r.config.MaxConcurrentRetrievals,
		func(ctx context.Context, _ int, retriever Retriever) (Candidates, error) {
			return retrieve(ctx, query, retriever.Retrieve)
		})
	if err != nil {
		return nil, err
	}
	return fuseRankings(ctx, rankings, r.config.RankConstant)
}

func fuseRankings(ctx context.Context, rankings []Candidates, rankConstant int) (Candidates, error) {
	positions := make(map[string]int)
	var fused Candidates

	for _, ranking := range rankings {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		seen := make(map[string]struct{}, len(ranking))
		for index, candidate := range ranking {
			identity := candidate.Document.ID
			if identity != "" {
				if _, duplicate := seen[identity]; duplicate {
					continue
				}
				seen[identity] = struct{}{}
			}

			contribution := Score(1 / (float64(rankConstant) + float64(index) + 1))
			if identity == "" {
				candidate.Score = contribution
				fused = append(fused, candidate)
				continue
			}
			position, found := positions[identity]
			if found {
				fused[position].Score += contribution
				continue
			}
			candidate.Score = contribution
			positions[identity] = len(fused)
			fused = append(fused, candidate)
		}
	}

	sortCandidatesByScore(fused)
	return fused, nil
}
