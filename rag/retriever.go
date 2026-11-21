package rag

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/samber/lo"
	"golang.org/x/sync/errgroup"
)

// retrieve checks an external stage's result. Callers establish query validity
// at their public entry point before invoking any stage.
func retrieve(ctx context.Context, query Query, call RetrieverFunc) (Candidates, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if call == nil {
		return nil, ErrNilRetriever
	}
	candidates, err := call(ctx, query)
	if err != nil {
		return nil, err
	}
	if err := candidates.Validate(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return candidates, nil
}

// PipelineRetriever owns the public entry boundary. Its pipeline checks each
// external stage result before passing it onward, so returning that same result
// does not require another candidate traversal. Construct it with Transform,
// Expand, Fuse or Refine; its zero value is invalid.
type PipelineRetriever struct {
	run func(context.Context, Query) (Candidates, error)
}

func (p *PipelineRetriever) Retrieve(ctx context.Context, query Query) (Candidates, error) {
	if p == nil || p.run == nil {
		return nil, ErrNilRetriever
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := query.Validate(); err != nil {
		return nil, err
	}
	candidates, err := p.run(ctx, query)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return candidates, nil
}

// Transform returns a [Retriever] that rewrites the query through
// transformers before calling next.
func Transform(next Retriever, transformers ...Transformer) (*PipelineRetriever, error) {
	if lo.IsNil(next) {
		return nil, ErrNilRetriever
	}
	owned := slices.Clone(transformers)
	for index, transformer := range owned {
		if lo.IsNil(transformer) {
			return nil, fmt.Errorf("rag: transformer %d: %w", index, ErrNilTransformer)
		}
	}

	return &PipelineRetriever{run: func(ctx context.Context, query Query) (Candidates, error) {
		current := query
		for i, transformer := range owned {
			transformed, err := transformQuery(ctx, transformer, current)
			if err != nil {
				return nil, fmt.Errorf("rag: transformer %d: %w", i, err)
			}
			current = transformed
		}
		return retrieve(ctx, current, next.Retrieve)
	}}, nil
}

type ExpansionConfig struct {
	Retriever Retriever
	Expander  Expander
	Fusion    ReciprocalRankFusionConfig
	// MaxConcurrentRetrievals bounds expanded-query retrievals per invocation.
	// Zero uses DefaultMaxConcurrentRetrievals; one is sequential.
	MaxConcurrentRetrievals int
}

// Expand returns a [Retriever] that retrieves each expanded query under
// the configured concurrency bound and combines rankings with reciprocal-rank
// fusion. Every query must succeed; the first failure cancels the remaining
// retrievals and is returned. Raw scores never cross query boundaries.
func Expand(config ExpansionConfig) (*PipelineRetriever, error) {
	if lo.IsNil(config.Retriever) {
		return nil, ErrNilRetriever
	}
	if lo.IsNil(config.Expander) {
		return nil, ErrNilExpander
	}
	fusion, err := config.Fusion.normalize()
	if err != nil {
		return nil, err
	}

	concurrency, err := normalizeRetrievalConcurrency(config.MaxConcurrentRetrievals)
	if err != nil {
		return nil, err
	}

	return &PipelineRetriever{run: func(ctx context.Context, query Query) (Candidates, error) {
		queries, err := expandQuery(ctx, config.Expander, query)
		if err != nil {
			return nil, fmt.Errorf("rag: expand query: %w", err)
		}
		rankings, err := parallelResults(ctx, "rag: expand", queries, "query", concurrency,
			func(ctx context.Context, q Query) (Candidates, error) {
				return retrieve(ctx, q, config.Retriever.Retrieve)
			})
		if err != nil {
			return nil, err
		}
		return fuseRankings(ctx, rankings, fusion.RankConstant)
	}}, nil
}

// Refine returns a [Retriever] that calls next and then applies
// refiners to the returned documents in order.
func Refine(next Retriever, refiners ...Refiner) (*PipelineRetriever, error) {
	if lo.IsNil(next) {
		return nil, ErrNilRetriever
	}
	owned := slices.Clone(refiners)
	for index, refiner := range owned {
		if lo.IsNil(refiner) {
			return nil, fmt.Errorf("rag: refiner %d: %w", index, ErrNilRefiner)
		}
	}

	return &PipelineRetriever{run: func(ctx context.Context, query Query) (Candidates, error) {
		docs, err := retrieve(ctx, query, next.Retrieve)
		if err != nil {
			return nil, err
		}
		for i, refiner := range owned {
			docs, err = refineCandidates(ctx, refiner, query, docs)
			if err != nil {
				return nil, fmt.Errorf("rag: refiner %d: %w", i, err)
			}
		}
		return docs, nil
	}}, nil
}

func transformQuery(ctx context.Context, transformer Transformer, query Query) (Query, error) {
	if err := ctx.Err(); err != nil {
		return Query{}, err
	}
	transformed, err := transformer.Transform(ctx, query)
	if err != nil {
		return Query{}, err
	}
	if err := transformed.Validate(); err != nil {
		return Query{}, fmt.Errorf("invalid transformed query: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return Query{}, err
	}
	return transformed, nil
}

func expandQuery(ctx context.Context, expander Expander, query Query) ([]Query, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	queries, err := expander.Expand(ctx, query)
	if err != nil {
		return nil, err
	}
	if len(queries) == 0 {
		return nil, ErrEmptyExpansion
	}
	seen := make(map[string]int, len(queries))
	for index, expanded := range queries {
		if err := expanded.Validate(); err != nil {
			return nil, fmt.Errorf("%w: query %d: %w", ErrInvalidExpansion, index, err)
		}
		if first, duplicate := seen[expanded.Text()]; duplicate {
			return nil, fmt.Errorf("%w: queries %d and %d have the same text", ErrInvalidExpansion, first, index)
		}
		seen[expanded.Text()] = index
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return queries, nil
}

func refineCandidates(ctx context.Context, refiner Refiner, query Query, candidates Candidates) (Candidates, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	refined, err := refiner.Refine(ctx, query, candidates)
	if err != nil {
		return nil, err
	}
	if err := refined.Validate(); err != nil {
		return nil, fmt.Errorf("invalid refined candidates: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return refined, nil
}

const DefaultMaxConcurrentRetrievals = 4

var ErrInvalidRetrievalConcurrency = errors.New("rag: retrieval concurrency must not be negative")

func normalizeRetrievalConcurrency(limit int) (int, error) {
	if limit < 0 {
		return 0, ErrInvalidRetrievalConcurrency
	}
	if limit == 0 {
		return DefaultMaxConcurrentRetrievals, nil
	}
	return limit, nil
}

func parallelResults[Item, Out any](
	ctx context.Context,
	op string,
	items []Item,
	itemLabel string,
	maxConcurrent int,
	fn func(context.Context, Item) (Out, error),
) ([]Out, error) {
	// Any failure fails the whole call, so the first one cancels the rest of the
	// fan-out instead of letting siblings spend work on a discarded result. The
	// limit is acquired before a goroutine starts, so the expanded query count
	// cannot determine how many are active.
	group, fanout := errgroup.WithContext(ctx)
	group.SetLimit(maxConcurrent)
	// Indexed results preserve ranking order independently of completion order.
	results := make([]Out, len(items))
	for index, item := range items {
		if fanout.Err() != nil {
			break
		}
		group.Go(func() error {
			if err := fanout.Err(); err != nil {
				return err
			}
			result, err := fn(fanout, item)
			if err != nil {
				return fmt.Errorf("%s #%d: %w", itemLabel, index, err)
			}
			results[index] = result
			return nil
		})
	}
	failure := group.Wait()
	// A caller's cancellation is the cause; failures after it are consequences.
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("%s: %w", op, err)
	}
	if failure != nil {
		return nil, fmt.Errorf("%s: %w", op, failure)
	}
	return results, nil
}
