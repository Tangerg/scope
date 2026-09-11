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

// ReciprocalRankFusionConfig controls reciprocal-rank weighting.
// RankConstant is added to each one-based rank before reciprocal weighting;
// zero uses [DefaultReciprocalRankConstant].
type ReciprocalRankFusionConfig struct {
	RankConstant int
}

func (r ReciprocalRankFusionConfig) normalized() (ReciprocalRankFusionConfig, error) {
	if r.RankConstant < 0 {
		return ReciprocalRankFusionConfig{}, ErrInvalidRankConstant
	}
	if r.RankConstant == 0 {
		r.RankConstant = DefaultReciprocalRankConstant
	}
	return r, nil
}

// FusionRetrieverConfig selects ranking policy and retrieval scheduling.
type FusionRetrieverConfig struct {
	Fusion ReciprocalRankFusionConfig
	// MaxConcurrentRetrievals bounds active child calls independently per
	// invocation. Zero uses DefaultMaxConcurrentRetrievals; one is sequential.
	MaxConcurrentRetrievals int
}

// ReciprocalRankFusion returns a retriever that concurrently executes each
// input retriever and fuses their ordered results using reciprocal-rank
// fusion. Raw candidate scores are deliberately ignored because independent
// retrievers commonly use incomparable score scales. Every retriever must
// succeed; failures are reported in declaration order without partial results.
func ReciprocalRankFusion(config FusionRetrieverConfig, retrievers ...Retriever) (Retriever, error) {
	fusion, err := config.Fusion.normalized()
	if err != nil {
		return nil, err
	}
	concurrency, err := normalizeRetrievalConcurrency(config.MaxConcurrentRetrievals)
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

	return reciprocalRankFusion{fusion: fusion, maxConcurrentRetrievals: concurrency, retrievers: owned}, nil
}

type reciprocalRankFusion struct {
	fusion                  ReciprocalRankFusionConfig
	maxConcurrentRetrievals int
	retrievers              []Retriever
}

func (r reciprocalRankFusion) Retrieve(ctx context.Context, query Query) (candidates Candidates, err error) {
	if validateErr := query.Validate(); validateErr != nil {
		return nil, validateErr
	}
	rankings, err := parallelResults(ctx, "rag.ReciprocalRankFusion", r.retrievers, "retriever", r.maxConcurrentRetrievals,
		func(ctx context.Context, _ int, retriever Retriever) (Candidates, error) {
			return retrieve(ctx, query, retriever.Retrieve)
		})
	if err != nil {
		return nil, err
	}
	return fuseRankings(ctx, rankings, r.fusion.RankConstant)
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
