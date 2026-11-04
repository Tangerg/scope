package rag_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Tangerg/scope/rag"
)

func testQuery(t *testing.T) rag.Query {
	t.Helper()
	query, err := rag.NewQuery("what is GOAP?")
	if err != nil {
		t.Fatal(err)
	}
	return query
}

func TestIdentityAugmenterValidatesItsInput(t *testing.T) {
	var invalid rag.Query
	valid := testQuery(t)
	if _, err := rag.IdentityAugmenter().Augment(t.Context(), invalid, nil); err == nil {
		t.Fatal("an invalid query passed through")
	}
	augmentation, err := rag.IdentityAugmenter().Augment(t.Context(), valid, nil)
	if err != nil {
		t.Fatal(err)
	}
	if text, replace := augmentation.ReplacementText(); replace || text != "" {
		t.Fatalf("identity produced replacement %q, %v", text, replace)
	}
}

func TestIdentityAugmenterObservesCancellation(t *testing.T) {
	valid := testQuery(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if _, err := rag.IdentityAugmenter().Augment(ctx, valid, nil); !errors.Is(err, context.Canceled) {
		t.Errorf("Augment error = %v", err)
	}
}
