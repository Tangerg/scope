package rag

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/samber/lo"
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

// composedRetriever owns the public entry boundary. Its pipeline checks each
// external stage result before passing it onward, so returning that same result
// does not require another candidate traversal.
type composedRetriever func(context.Context, Query) (Candidates, error)

func (c composedRetriever) Retrieve(ctx context.Context, query Query) (Candidates, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := query.Validate(); err != nil {
		return nil, err
	}
	candidates, err := c(ctx, query)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return candidates, nil
}

// WithTransformers returns a [Retriever] that rewrites the query through
// transformers before calling next.
func WithTransformers(next Retriever, transformers ...Transformer) (Retriever, error) {
	if lo.IsNil(next) {
		return nil, ErrNilRetriever
	}
	owned := slices.Clone(transformers)
	for index, transformer := range owned {
		if lo.IsNil(transformer) {
			return nil, fmt.Errorf("rag.WithTransformers: transformer %d: %w", index, ErrNilTransformer)
		}
	}

	return composedRetriever(func(ctx context.Context, query Query) (Candidates, error) {
		current := query
		for i, transformer := range owned {
			transformed, err := transform(ctx, transformer, current)
			if err != nil {
				return nil, fmt.Errorf("rag: transformer %d: %w", i, err)
			}
			current = transformed
		}
		return retrieve(ctx, current, next.Retrieve)
	}), nil
}

// ExpansionConfig selects the query expansion stage, retrieval source, and
// rank fusion policy used to combine the independent query results.
type ExpansionConfig struct {
	Retriever Retriever
	Expander  Expander
	Fusion    ReciprocalRankFusionConfig
	// MaxConcurrentRetrievals bounds expanded-query retrievals per invocation.
	// Zero uses DefaultMaxConcurrentRetrievals; one is sequential.
	MaxConcurrentRetrievals int
}

// WithExpander returns a [Retriever] that retrieves each expanded query under
// the configured concurrency bound and combines rankings with reciprocal-rank
// fusion. Every query must succeed; raw scores never cross query boundaries.
func WithExpander(config ExpansionConfig) (Retriever, error) {
	if lo.IsNil(config.Retriever) {
		return nil, ErrNilRetriever
	}
	if lo.IsNil(config.Expander) {
		return nil, ErrNilExpander
	}
	fusion, err := config.Fusion.normalized()
	if err != nil {
		return nil, err
	}

	concurrency, err := normalizeRetrievalConcurrency(config.MaxConcurrentRetrievals)
	if err != nil {
		return nil, err
	}

	return composedRetriever(func(ctx context.Context, query Query) (Candidates, error) {
		queries, err := expand(ctx, config.Expander, query)
		if err != nil {
			return nil, fmt.Errorf("rag: expand query: %w", err)
		}
		rankings, err := parallelResults(ctx, "rag.WithExpander", queries, "query", concurrency,
			func(ctx context.Context, _ int, q Query) (Candidates, error) {
				return retrieve(ctx, q, config.Retriever.Retrieve)
			})
		if err != nil {
			return nil, err
		}
		return fuseRankings(ctx, rankings, fusion.RankConstant)
	}), nil
}

// WithRefiners returns a [Retriever] that calls next and then applies
// refiners to the returned documents in order.
func WithRefiners(next Retriever, refiners ...Refiner) (Retriever, error) {
	if lo.IsNil(next) {
		return nil, ErrNilRetriever
	}
	owned := slices.Clone(refiners)
	for index, refiner := range owned {
		if lo.IsNil(refiner) {
			return nil, fmt.Errorf("rag.WithRefiners: refiner %d: %w", index, ErrNilRefiner)
		}
	}

	return composedRetriever(func(ctx context.Context, query Query) (Candidates, error) {
		docs, err := retrieve(ctx, query, next.Retrieve)
		if err != nil {
			return nil, err
		}
		for i, refiner := range owned {
			docs, err = refine(ctx, refiner, query, docs)
			if err != nil {
				return nil, fmt.Errorf("rag: refiner %d: %w", i, err)
			}
		}
		return docs, nil
	}), nil
}

func transform(ctx context.Context, transformer Transformer, query Query) (Query, error) {
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

func expand(ctx context.Context, expander Expander, query Query) ([]Query, error) {
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

func refine(ctx context.Context, refiner Refiner, query Query, candidates Candidates) (Candidates, error) {
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

// DefaultMaxConcurrentRetrievals bounds fan-out when no limit is specified.
const DefaultMaxConcurrentRetrievals = 4

// ErrInvalidRetrievalConcurrency rejects a negative retrieval concurrency bound.
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
	fn func(context.Context, int, Item) (Out, error),
) ([]Out, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	// Indexed results preserve ranking and failure order independently of
	// completion order. Acquire a slot before starting a goroutine so expanded
	// query count cannot determine the number of active goroutines.
	results := make([]Out, len(items))
	failures := make([]error, len(items))

	var wg sync.WaitGroup
	slots := make(chan struct{}, min(maxConcurrent, len(items)))
	for index, item := range items {
		slots <- struct{}{}
		wg.Go(func() {
			defer func() { <-slots }()
			result, err := fn(ctx, index, item)
			if err != nil {
				failures[index] = fmt.Errorf("%s #%d: %w", itemLabel, index, err)
				return
			}
			results[index] = result
		})
	}
	wg.Wait()

	if err := errors.Join(failures...); err != nil {
		return nil, fmt.Errorf("%s: %w", op, err)
	}

	return results, nil
}
