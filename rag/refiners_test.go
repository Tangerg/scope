package rag_test

import (
	"context"
	"testing"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/rag"
)

func TestDedupKeepsHighestScoreAtFirstIdentityPosition(t *testing.T) {
	r := rag.Dedup()

	low, _ := document.NewDocument("low", nil)
	low.ID = "1"
	b, _ := document.NewDocument("b", nil)
	b.ID = "2"
	high, _ := document.NewDocument("high", nil)
	high.ID = "1"

	got, err := r.Refine(t.Context(), mustQuery(t, "query"), []rag.Candidate{
		candidate(low, 0.2),
		candidate(b, 0.6),
		candidate(high, 0.9),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d docs, want 2", len(got))
	}
	if got[0].Document.Text != high.Text || got[0].Document == high || got[0].Score != 0.9 {
		t.Fatalf("first identity representative = %#v, want highest-scoring candidate", got[0])
	}
	if got[1].Document.Text != b.Text {
		t.Fatalf("identity order broken: second document = %#v", got[1].Document)
	}
}

func TestDedupEqualScoresKeepFirstCandidate(t *testing.T) {
	first, _ := document.NewDocument("first", nil)
	first.ID = "same"
	second, _ := document.NewDocument("second", nil)
	second.ID = "same"

	got, err := rag.Dedup().Refine(t.Context(), mustQuery(t, "query"), []rag.Candidate{
		candidate(first, 0.8),
		candidate(second, 0.8),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Document.Text != first.Text || got[0].Document == first {
		t.Fatalf("equal-score representative = %#v, want first candidate", got)
	}
}

func TestDedupKeepsDocumentsWithoutIdentityDistinct(t *testing.T) {
	first, _ := document.NewDocument("first", nil)
	second, _ := document.NewDocument("second", nil)

	got, err := rag.Dedup().Refine(t.Context(), mustQuery(t, "query"), []rag.Candidate{
		candidate(first, 0.8),
		candidate(second, 0.9),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Document.Text != first.Text || got[1].Document.Text != second.Text ||
		got[0].Document == first || got[1].Document == second {
		t.Fatalf("identity-free documents were collapsed: %#v", got)
	}
}

func TestDedupHonorsContextCancel(t *testing.T) {
	r := rag.Dedup()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if _, err := r.Refine(ctx, mustQuery(t, "query"), nil); err == nil {
		t.Fatal("canceled ctx must error")
	}
}

func TestTopKSortsAndCaps(t *testing.T) {
	r, err := rag.TopK(2)
	if err != nil {
		t.Fatal(err)
	}

	aDoc, _ := document.NewDocument("a", nil)
	bDoc, _ := document.NewDocument("b", nil)
	cDoc, _ := document.NewDocument("c", nil)
	a := candidate(aDoc, 0.3)
	b := candidate(bDoc, 0.9)
	c := candidate(cDoc, 0.5)

	got, err := r.Refine(t.Context(), mustQuery(t, "query"), []rag.Candidate{a, b, c})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d docs, want 2 (capped to topK)", len(got))
	}
	if got[0].Score != 0.9 || got[1].Score != 0.5 {
		t.Fatalf("sort order broken: %v, %v", got[0].Score, got[1].Score)
	}
}

func TestTopKPreservesDuplicateCandidates(t *testing.T) {
	r, err := rag.TopK(2)
	if err != nil {
		t.Fatal(err)
	}

	low, _ := document.NewDocument("duplicate low", nil)
	low.ID = "a"
	high, _ := document.NewDocument("duplicate high", nil)
	high.ID = "a"
	other, _ := document.NewDocument("other", nil)
	other.ID = "b"

	got, err := r.Refine(t.Context(), mustQuery(t, "query"), []rag.Candidate{
		candidate(low, 0.8),
		candidate(high, 0.9),
		candidate(other, 0.7),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d documents, want two highest-scoring candidates", len(got))
	}
	if got[0].Document.Text != high.Text || got[1].Document.Text != low.Text {
		t.Fatalf("results = %#v, want both candidates with identity a", got)
	}
}

func TestDedupBeforeTopKKeepsUniqueResultSlots(t *testing.T) {
	top, err := rag.TopK(2)
	if err != nil {
		t.Fatal(err)
	}
	first, _ := document.NewDocument("first low", nil)
	first.ID = "a"
	second, _ := document.NewDocument("second", nil)
	second.ID = "b"
	best, _ := document.NewDocument("first best", nil)
	best.ID = "a"
	third, _ := document.NewDocument("third", nil)
	third.ID = "c"
	input := []rag.Candidate{
		candidate(first, 0.85),
		candidate(second, 0.8),
		candidate(best, 0.9),
		candidate(third, 0.7),
	}

	query := mustQuery(t, "query")
	deduped, err := rag.Dedup().Refine(t.Context(), query, input)
	if err != nil {
		t.Fatal(err)
	}
	dedupThenTop, err := top.Refine(t.Context(), query, deduped)
	if err != nil {
		t.Fatal(err)
	}
	topped, err := top.Refine(t.Context(), query, input)
	if err != nil {
		t.Fatal(err)
	}
	topThenDedup, err := rag.Dedup().Refine(t.Context(), query, topped)
	if err != nil {
		t.Fatal(err)
	}

	if len(dedupThenTop) != 2 || dedupThenTop[0].Document.ID != "a" || dedupThenTop[1].Document.ID != "b" {
		t.Fatalf("dedup before top = %#v, want a then b", dedupThenTop)
	}
	if len(topThenDedup) != 1 || topThenDedup[0].Document.ID != "a" {
		t.Fatalf("top before dedup = %#v, want only a", topThenDedup)
	}
}

func TestTopKRejectsNonPositiveLimit(t *testing.T) {
	for _, limit := range []int{0, -1} {
		if _, err := rag.TopK(limit); err == nil {
			t.Fatalf("TopK(%d) succeeded, want error", limit)
		}
	}
}

func TestTopKDoesNotMutateInput(t *testing.T) {
	r, err := rag.TopK(10)
	if err != nil {
		t.Fatal(err)
	}

	aDoc, _ := document.NewDocument("a", nil)
	bDoc, _ := document.NewDocument("b", nil)
	a := candidate(aDoc, 0.1)
	b := candidate(bDoc, 0.9)
	in := []rag.Candidate{a, b}

	got, err := r.Refine(t.Context(), mustQuery(t, "query"), in)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Document.Text != "b" || got[1].Document.Text != "a" {
		t.Fatalf("ranked candidates = %#v, want b then a", got)
	}
	got[0].Document.Text = "changed"

	if in[0].Score != 0.1 || in[1].Score != 0.9 {
		t.Fatalf("input mutated: %v %v", in[0].Score, in[1].Score)
	}
	if aDoc.Text != "a" || bDoc.Text != "b" {
		t.Fatal("ranked output aliases input documents")
	}
}
