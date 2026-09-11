package rag_test

import (
	"errors"
	"math"
	"testing"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/rag"
)

func TestReciprocalRankFusionUsesRanksInsteadOfRawScores(t *testing.T) {
	firstA := identifiedDocument(t, "a", "a from first retriever")
	secondA := identifiedDocument(t, "a", "a from second retriever")
	b := identifiedDocument(t, "b", "b")
	c := identifiedDocument(t, "c", "c")

	fused, err := rag.ReciprocalRankFusion(
		rag.FusionRetrieverConfig{},
		&fakeRetriever{docs: []rag.Candidate{candidate(firstA, 0.01), candidate(b, 1_000)}},
		&fakeRetriever{docs: []rag.Candidate{candidate(secondA, -100), candidate(c, 10_000)}},
	)
	if err != nil {
		t.Fatal(err)
	}

	got, err := fused.Retrieve(t.Context(), mustQuery(t, "query"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("fused candidates = %d, want 3", len(got))
	}
	if got[0].Document.Text != firstA.Text || got[0].Document == firstA {
		t.Fatalf("top candidate = %#v, want first representative of identity a", got[0].Document)
	}
	want := 2.0 / float64(rag.DefaultReciprocalRankConstant+1)
	if math.Abs(got[0].Score.Float64()-want) > 1e-12 {
		t.Fatalf("fused score = %v, want %v", got[0].Score, want)
	}
	if got[1].Document.ID != b.ID || got[2].Document.ID != c.ID {
		t.Fatalf("equal-score order = %q, %q; want b, c", got[1].Document.ID, got[2].Document.ID)
	}
}

func TestReciprocalRankFusionDoesNotRewardDuplicateIdentityWithinOneRanking(t *testing.T) {
	firstA := identifiedDocument(t, "a", "a first")
	duplicateA := identifiedDocument(t, "a", "a duplicate")
	b := identifiedDocument(t, "b", "b")

	fused, err := rag.ReciprocalRankFusion(
		rag.FusionRetrieverConfig{Fusion: rag.ReciprocalRankFusionConfig{RankConstant: 10}},
		&fakeRetriever{docs: []rag.Candidate{candidate(firstA), candidate(duplicateA), candidate(b)}},
		&fakeRetriever{docs: []rag.Candidate{candidate(b)}},
	)
	if err != nil {
		t.Fatal(err)
	}

	got, err := fused.Retrieve(t.Context(), mustQuery(t, "query"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Document.ID != b.ID || got[1].Document.Text != firstA.Text {
		t.Fatalf("fused order = %#v, want b then the first a", got)
	}
	wantA := 1.0 / 11.0
	if math.Abs(got[1].Score.Float64()-wantA) > 1e-12 {
		t.Fatalf("duplicate identity changed score: got %v, want %v", got[1].Score, wantA)
	}
}

func TestReciprocalRankFusionUsesStableFirstAppearanceForTies(t *testing.T) {
	a := identifiedDocument(t, "a", "a")
	b := identifiedDocument(t, "b", "b")
	fused, err := rag.ReciprocalRankFusion(
		rag.FusionRetrieverConfig{Fusion: rag.ReciprocalRankFusionConfig{RankConstant: 10}},
		&fakeRetriever{docs: []rag.Candidate{candidate(a), candidate(b)}},
		&fakeRetriever{docs: []rag.Candidate{candidate(b), candidate(a)}},
	)
	if err != nil {
		t.Fatal(err)
	}

	got, err := fused.Retrieve(t.Context(), mustQuery(t, "query"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Document.ID != a.ID || got[1].Document.ID != b.ID || got[0].Score != got[1].Score {
		t.Fatalf("tie order = %#v, want a then b with equal scores", got)
	}
}

func TestReciprocalRankFusionValidatesConfigurationAndChildren(t *testing.T) {
	if _, err := rag.ReciprocalRankFusion(rag.FusionRetrieverConfig{Fusion: rag.ReciprocalRankFusionConfig{RankConstant: -1}}, &fakeRetriever{}); !errors.Is(err, rag.ErrInvalidRankConstant) {
		t.Fatalf("invalid rank constant error = %v", err)
	}
	if _, err := rag.ReciprocalRankFusion(rag.FusionRetrieverConfig{Fusion: rag.ReciprocalRankFusionConfig{RankConstant: 10}}); !errors.Is(err, rag.ErrNilRetriever) {
		t.Fatalf("empty retrievers error = %v", err)
	}
	if _, err := rag.ReciprocalRankFusion(rag.FusionRetrieverConfig{Fusion: rag.ReciprocalRankFusionConfig{RankConstant: 10}}, nil); !errors.Is(err, rag.ErrNilRetriever) {
		t.Fatalf("nil retriever error = %v", err)
	}

	fused, err := rag.ReciprocalRankFusion(
		rag.FusionRetrieverConfig{Fusion: rag.ReciprocalRankFusionConfig{RankConstant: 10}},
		&fakeRetriever{docs: []rag.Candidate{{}}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fused.Retrieve(t.Context(), mustQuery(t, "query")); !errors.Is(err, rag.ErrInvalidCandidate) {
		t.Fatalf("invalid child candidate error = %v", err)
	}
}

func TestReciprocalRankFusionDoesNotOverflowLargeRankConstant(t *testing.T) {
	doc := identifiedDocument(t, "doc", "document")
	fused, err := rag.ReciprocalRankFusion(
		rag.FusionRetrieverConfig{Fusion: rag.ReciprocalRankFusionConfig{RankConstant: int(^uint(0) >> 1)}},
		&fakeRetriever{docs: []rag.Candidate{candidate(doc)}},
	)
	if err != nil {
		t.Fatal(err)
	}
	got, err := fused.Retrieve(t.Context(), mustQuery(t, "query"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Score <= 0 || math.IsNaN(got[0].Score.Float64()) || math.IsInf(got[0].Score.Float64(), 0) {
		t.Fatalf("fused result = %#v", got)
	}
}

func identifiedDocument(t *testing.T, id, text string) *document.Document {
	t.Helper()
	doc, err := document.NewDocument(text, nil)
	if err != nil {
		t.Fatal(err)
	}
	doc.ID = id
	return doc
}
