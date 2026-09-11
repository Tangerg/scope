package rag_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Tangerg/scope/rag"
)

func TestComposedRetrieversRejectInputBeforeCallingStages(t *testing.T) {
	for _, name := range []string{"fusion", "transformers", "expander", "refiners"} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			base := rag.RetrieverFunc(func(context.Context, rag.Query) (rag.Candidates, error) {
				calls++
				return nil, nil
			})
			var composed rag.Retriever
			var err error
			switch name {
			case "fusion":
				composed, err = rag.ReciprocalRankFusion(rag.FusionRetrieverConfig{}, base)
			case "transformers":
				composed, err = rag.WithTransformers(base, rag.TransformerFunc(func(_ context.Context, query rag.Query) (rag.Query, error) {
					calls++
					return query, nil
				}))
			case "expander":
				composed, err = rag.WithExpander(rag.ExpansionConfig{Retriever: base, Expander: rag.ExpanderFunc(func(_ context.Context, query rag.Query) ([]rag.Query, error) {
					calls++
					return []rag.Query{query}, nil
				})})
			case "refiners":
				composed, err = rag.WithRefiners(base, rag.Dedup())
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := composed.Retrieve(t.Context(), rag.Query{}); !errors.Is(err, rag.ErrInvalidQuery) {
				t.Fatalf("invalid query error = %v", err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			if _, err := composed.Retrieve(ctx, mustQuery(t, "query")); !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled query error = %v", err)
			}
			if calls != 0 {
				t.Fatalf("stages invoked %d times for rejected input", calls)
			}
		})
	}
}

func TestComposedRetrieverDoesNotPublishResultsAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	base := rag.RetrieverFunc(func(context.Context, rag.Query) (rag.Candidates, error) { return nil, nil })
	composed, err := rag.WithRefiners(base, rag.RefinerFunc(func(context.Context, rag.Query, rag.Candidates) (rag.Candidates, error) {
		cancel()
		return nil, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := composed.Retrieve(ctx, mustQuery(t, "query")); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled result error = %v", err)
	}
}
