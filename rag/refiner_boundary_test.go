package rag_test

import (
	"context"
	"errors"
	"math"
	"testing"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/rag"
)

func TestRefinersRejectInvalidQueryCandidatesAndCancellation(t *testing.T) {
	top, err := rag.TopK(1)
	if err != nil {
		t.Fatal(err)
	}
	for _, refiner := range []rag.Refiner{rag.Dedup(), top} {
		if output, err := refiner.Refine(t.Context(), rag.Query{}, nil); output != nil || !errors.Is(err, rag.ErrInvalidQuery) {
			t.Fatalf("invalid query = %v, %v", output, err)
		}
		query := mustQuery(t, "query")
		for _, candidate := range []rag.Candidate{
			{Document: &document.Document{Text: "bad\xff"}},
			{Document: &document.Document{Text: "text"}, Score: rag.Score(math.NaN())},
		} {
			if output, err := refiner.Refine(t.Context(), query, rag.Candidates{candidate}); output != nil || !errors.Is(err, rag.ErrInvalidCandidate) {
				t.Fatalf("invalid candidate = %v, %v", output, err)
			}
		}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if output, err := refiner.Refine(ctx, query, nil); output != nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled refinement = %v, %v", output, err)
		}
	}
}

func TestTransformerFailureStopsCompositionBeforeRetrieval(t *testing.T) {
	cause := errors.New("transform failed")
	for _, invalid := range []bool{false, true} {
		composed, err := rag.WithTransformers(
			rag.RetrieverFunc(func(context.Context, rag.Query) (rag.Candidates, error) {
				t.Fatal("invalid transformation reached retrieval")
				return nil, nil
			}),
			rag.TransformerFunc(func(context.Context, rag.Query) (rag.Query, error) {
				if invalid {
					return rag.Query{}, nil
				}
				return rag.Query{}, cause
			}),
		)
		if err != nil {
			t.Fatal(err)
		}
		want := cause
		if invalid {
			want = rag.ErrInvalidQuery
		}
		if output, err := composed.Retrieve(t.Context(), mustQuery(t, "query")); output != nil || !errors.Is(err, want) {
			t.Fatalf("Retrieve = %v, %v", output, err)
		}
	}
}
