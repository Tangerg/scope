package rag_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/Tangerg/scope/rag"
)

func TestExpansionUniquenessBoundary(t *testing.T) {
	var calls atomic.Int64
	retriever := rag.RetrieverFunc(func(context.Context, rag.Query) (rag.Candidates, error) { calls.Add(1); return nil, nil })
	expander := rag.ExpanderFunc(func(_ context.Context, q rag.Query) ([]rag.Query, error) { return []rag.Query{q, q}, nil })
	wrapped, err := rag.WithExpander(rag.ExpansionConfig{Retriever: retriever, Expander: expander})
	if err != nil {
		t.Fatal(err)
	}
	query, err := rag.NewQuery("same query")
	if err != nil {
		t.Fatal(err)
	}
	_, err = wrapped.Retrieve(t.Context(), query)
	if !errors.Is(err, rag.ErrInvalidExpansion) {
		t.Fatalf("duplicate expansion admitted: retrieval calls=%d error=%v", calls.Load(), err)
	}
	if calls.Load() != 0 {
		t.Fatalf("invalid expansion performed %d retrieval calls", calls.Load())
	}
}
