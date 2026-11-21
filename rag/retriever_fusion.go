package rag

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/samber/lo"
)

var ErrInvalidRankConstant = errors.New("rag: reciprocal-rank constant must not be negative")

// DefaultReciprocalRankConstant is the conventional RRF smoothing constant.
const DefaultReciprocalRankConstant = 60

// ReciprocalRankFusionConfig sets the constant added to each one-based rank
// before reciprocal weighting; zero uses [DefaultReciprocalRankConstant].
type ReciprocalRankFusionConfig struct {
	RankConstant int
}

func (r ReciprocalRankFusionConfig) normalize() (ReciprocalRankFusionConfig, error) {
	if r.RankConstant < 0 {
		return ReciprocalRankFusionConfig{}, ErrInvalidRankConstant
	}
	if r.RankConstant == 0 {
		r.RankConstant = DefaultReciprocalRankConstant
	}
	return r, nil
}

// FusionConfig fuses the ordered results of several retrievers for one query.
type FusionConfig struct {
	Retrievers []Retriever
	Fusion     ReciprocalRankFusionConfig
	// MaxConcurrentRetrievals bounds active child calls independently per
	// invocation. Zero uses DefaultMaxConcurrentRetrievals; one is sequential.
	MaxConcurrentRetrievals int
}

// Fuse returns a [Retriever] that concurrently executes each configured
// retriever and fuses their ordered results using reciprocal-rank fusion. Raw
// candidate scores are deliberately ignored because independent retrievers
// commonly use incomparable score scales. Every retriever must succeed; the
// first failure cancels the remaining retrievals and is returned without
// partial results.
func Fuse(config FusionConfig) (*PipelineRetriever, error) {
	fusion, err := config.Fusion.normalize()
	if err != nil {
		return nil, err
	}
	concurrency, err := normalizeRetrievalConcurrency(config.MaxConcurrentRetrievals)
	if err != nil {
		return nil, err
	}
	if len(config.Retrievers) == 0 {
		return nil, fmt.Errorf("rag: fusion requires at least one retriever: %w", ErrNilRetriever)
	}
	retrievers := slices.Clone(config.Retrievers)
	for index, retriever := range retrievers {
		if lo.IsNil(retriever) {
			return nil, fmt.Errorf("rag: fusion retriever %d: %w", index, ErrNilRetriever)
		}
	}
	return &PipelineRetriever{run: func(ctx context.Context, query Query) (Candidates, error) {
		rankings, err := parallelResults(ctx, "rag: fuse", retrievers, "retriever", concurrency,
			func(ctx context.Context, _ int, retriever Retriever) (Candidates, error) {
				return retrieve(ctx, query, retriever.Retrieve)
			})
		if err != nil {
			return nil, err
		}
		return fuseRankings(ctx, rankings, fusion.RankConstant)
	}}, nil
}

func fuseRankings(ctx context.Context, rankings []Candidates, rankConstant int) (Candidates, error) {
	positions := make(map[documentKey]int)
	var fused Candidates

	for _, ranking := range rankings {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		seen := make(map[documentKey]struct{}, len(ranking))
		for index, candidate := range ranking {
			identity, err := candidate.documentKey()
			if err != nil {
				return nil, err
			}
			if identity.id != "" {
				if _, duplicate := seen[identity]; duplicate {
					continue
				}
				seen[identity] = struct{}{}
			}

			contribution := Score(1 / (float64(rankConstant) + float64(index) + 1))
			if identity.id == "" {
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

	fused.sortByScore()
	return fused, nil
}
