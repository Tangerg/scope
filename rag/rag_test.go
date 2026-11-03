package rag_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/rag"
)

func TestNewQuery_RequiresText(t *testing.T) {
	if _, err := rag.NewQuery(""); !errors.Is(err, rag.ErrInvalidQuery) {
		t.Fatalf("NewQuery error = %v, want ErrInvalidQuery", err)
	}
	query, err := rag.NewQuery("  query  ")
	if err != nil || query.Text() != "query" {
		t.Fatalf("normalized query = %q, %v", query.Text(), err)
	}
}

func TestAugmentationOwnsCitationOrder(t *testing.T) {
	doc, _ := document.NewDocument("evidence", nil)
	augmentation, err := (rag.Evidence{{Candidate: candidate(doc), Content: doc.Text}}).Augment("answer with ", "")
	if err != nil {
		t.Fatal(err)
	}
	copied := augmentation
	citations := copied.Citations().Candidates()
	citations[0].Document.Text = "mutated"
	if augmentation.Citations().Candidates()[0].Document.Text != "evidence" || doc.Text != "evidence" {
		t.Fatal("citation document aliases caller mutation")
	}
	copied, err = (rag.Evidence{{Candidate: candidate(identifiedDocument(t, "replacement", "other evidence")), Content: "other evidence"}}).Augment("before", "after")
	if err != nil {
		t.Fatal(err)
	}
	if copied.Citations().Candidates()[0].Document.Text != "other evidence" || augmentation.Citations().Candidates()[0].Document.Text != "evidence" {
		t.Fatal("replacing citations changed another augmentation")
	}
	if rag.CitationMarker(0) != "[1]" || rag.CitationMarker(1) != "[2]" {
		t.Fatalf("markers = %s, %s", rag.CitationMarker(0), rag.CitationMarker(1))
	}
	if _, err := (rag.Evidence{{Candidate: rag.Candidate{Score: 1}}}).Augment("", ""); !errors.Is(err, rag.ErrInvalidAugmentation) {
		t.Fatalf("invalid citation error = %v", err)
	}
}

func TestQueryValidateRejectsZeroValue(t *testing.T) {
	if err := new(rag.Query).Validate(); !errors.Is(err, rag.ErrInvalidQuery) {
		t.Fatalf("zero-value Query.Validate error = %v", err)
	}
}

type fakeRetriever struct {
	docs rag.Candidates
	err  error
	hits int
	got  string
}

func candidate(doc *document.Document, score ...float64) rag.Candidate {
	var value float64
	if len(score) > 0 {
		value = score[0]
	}
	return rag.Candidate{Document: doc, Score: rag.Score(value)}
}

func (f *fakeRetriever) Retrieve(_ context.Context, q rag.Query) (rag.Candidates, error) {
	f.hits++
	f.got = q.Text()
	if f.err != nil {
		return nil, f.err
	}
	return f.docs.Clone(), nil
}

func TestRetrieveValidatesCandidates(t *testing.T) {
	retriever := &fakeRetriever{docs: rag.Candidates{{}}}
	query, _ := rag.NewQuery("query")
	composed, err := rag.ReciprocalRankFusion(rag.FusionRetrieverConfig{}, rag.RetrieverFunc(retriever.Retrieve))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := composed.Retrieve(t.Context(), query); !errors.Is(err, rag.ErrInvalidCandidate) {
		t.Fatalf("invalid candidate error = %v", err)
	}
}

func TestRetrieveTransfersOwnedCandidates(t *testing.T) {
	doc, _ := document.NewDocument("source", nil)
	retriever := &fakeRetriever{docs: rag.Candidates{candidate(doc, 0.8)}}
	query, _ := rag.NewQuery("query")

	first, err := rag.RetrieverFunc(retriever.Retrieve).Retrieve(t.Context(), query)
	if err != nil {
		t.Fatal(err)
	}
	first[0].Document.Text = "mutated"
	second, err := rag.RetrieverFunc(retriever.Retrieve).Retrieve(t.Context(), query)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Text != "source" || second[0].Document.Text != "source" {
		t.Fatalf("retrieval candidates alias source: source=%q second=%q", doc.Text, second[0].Document.Text)
	}
}

func TestWithRefinersRejectsInvalidRetrieverOutputBeforeRefining(t *testing.T) {
	refinerCalled := false
	refined, err := rag.WithRefiners(
		&fakeRetriever{docs: rag.Candidates{{}}},
		rag.RefinerFunc(func(context.Context, rag.Query, rag.Candidates) (rag.Candidates, error) {
			refinerCalled = true
			return nil, nil
		}),
	)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := refined.Retrieve(t.Context(), mustQuery(t, "query")); !errors.Is(err, rag.ErrInvalidCandidate) {
		t.Fatalf("invalid candidate error = %v", err)
	}
	if refinerCalled {
		t.Fatal("refiner received invalid candidates")
	}
}

func TestWithRefinersRejectsInvalidOutputBeforeNextRefiner(t *testing.T) {
	secondCalled := false
	refined, err := rag.WithRefiners(
		&fakeRetriever{},
		rag.RefinerFunc(func(context.Context, rag.Query, rag.Candidates) (rag.Candidates, error) {
			return rag.Candidates{{}}, nil
		}),
		rag.RefinerFunc(func(context.Context, rag.Query, rag.Candidates) (rag.Candidates, error) {
			secondCalled = true
			return nil, nil
		}),
	)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := refined.Retrieve(t.Context(), mustQuery(t, "query")); !errors.Is(err, rag.ErrInvalidCandidate) {
		t.Fatalf("invalid candidate error = %v", err)
	}
	if secondCalled {
		t.Fatal("next refiner received invalid candidates")
	}
}

type fakeTransformer struct {
	suffix string
	err    error
}

func (f *fakeTransformer) Transform(_ context.Context, q rag.Query) (rag.Query, error) {
	if f.err != nil {
		return rag.Query{}, f.err
	}
	return rag.NewQuery(q.Text() + f.suffix)
}

func TestWithTransformersFeedsTransformedQueryToRetriever(t *testing.T) {
	doc, _ := document.NewDocument("retrieved-doc", nil)
	retriever := &fakeRetriever{docs: rag.Candidates{candidate(doc)}}

	r, err := rag.WithTransformers(retriever, &fakeTransformer{suffix: "?"})
	if err != nil {
		t.Fatal(err)
	}
	docs, err := r.Retrieve(t.Context(), mustQuery(t, "hi"))
	if err != nil {
		t.Fatal(err)
	}
	if retriever.got != "hi?" {
		t.Fatalf("retriever query = %q, want hi?", retriever.got)
	}
	if len(docs) != 1 || docs[0].Document.Text != doc.Text || docs[0].Document == doc {
		t.Fatalf("docs = %v", docs)
	}
	if retriever.hits != 1 {
		t.Fatalf("retriever hits = %d, want 1", retriever.hits)
	}
}

func TestWithTransformersErrorShortCircuits(t *testing.T) {
	want := errors.New("boom")
	retriever := &fakeRetriever{}

	r, err := rag.WithTransformers(retriever, &fakeTransformer{err: want})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Retrieve(t.Context(), mustQuery(t, "hi")); !errors.Is(err, want) {
		t.Fatalf("err = %v", err)
	}
	if retriever.hits != 0 {
		t.Fatal("retriever ran despite transformer failure")
	}
}

func TestFusionUnionsResults(t *testing.T) {
	docA, _ := document.NewDocument("a", nil)
	docB, _ := document.NewDocument("b", nil)
	r1 := &fakeRetriever{docs: rag.Candidates{candidate(docA)}}
	r2 := &fakeRetriever{docs: rag.Candidates{candidate(docB)}}

	combined, err := rag.ReciprocalRankFusion(rag.FusionRetrieverConfig{}, r1, r2)
	if err != nil {
		t.Fatal(err)
	}
	docs, err := combined.Retrieve(t.Context(), mustQuery(t, "hi"))
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 2 {
		t.Fatalf("got %d docs, want 2 (union of r1, r2)", len(docs))
	}
}

func TestFusionRejectsPartialResults(t *testing.T) {
	docA, _ := document.NewDocument("a", nil)
	r1 := &fakeRetriever{docs: rag.Candidates{candidate(docA)}}
	r2 := &fakeRetriever{err: errors.New("retriever 2 broken")}

	combined, err := rag.ReciprocalRankFusion(rag.FusionRetrieverConfig{}, r1, r2)
	if err != nil {
		t.Fatal(err)
	}
	docs, err := combined.Retrieve(t.Context(), mustQuery(t, "hi"))
	if err == nil {
		t.Fatal("partial retrieval must fail")
	}
	if docs != nil {
		t.Fatalf("docs = %#v, want nil on partial failure", docs)
	}
}

func TestFusionAcceptsEmptySuccessfulResults(t *testing.T) {
	combined, err := rag.ReciprocalRankFusion(rag.FusionRetrieverConfig{}, &fakeRetriever{}, &fakeRetriever{})
	if err != nil {
		t.Fatal(err)
	}

	docs, err := combined.Retrieve(t.Context(), mustQuery(t, "hi"))
	if err != nil {
		t.Fatal(err)
	}
	if docs != nil {
		t.Fatalf("docs = %#v, want nil", docs)
	}
}

func TestFusionOwnsConfiguredRetrievers(t *testing.T) {
	failure := errors.New("configured failure")
	retrievers := []rag.Retriever{&fakeRetriever{err: failure}}
	combined, err := rag.ReciprocalRankFusion(rag.FusionRetrieverConfig{}, retrievers...)
	if err != nil {
		t.Fatal(err)
	}
	retrievers[0] = &fakeRetriever{}

	if _, err := combined.Retrieve(t.Context(), mustQuery(t, "hi")); !errors.Is(err, failure) {
		t.Fatalf("ReciprocalRankFusion error = %v, want configured failure", err)
	}
}

func TestCombinatorsRejectNilCapabilitiesAtConstruction(t *testing.T) {
	var transformer *fakeTransformer
	if _, err := rag.WithTransformers(&fakeRetriever{}, transformer); !errors.Is(err, rag.ErrNilTransformer) {
		t.Fatalf("WithTransformers error = %v, want ErrNilTransformer", err)
	}
	if _, err := rag.WithExpander(rag.ExpansionConfig{Retriever: &fakeRetriever{}}); !errors.Is(err, rag.ErrNilExpander) {
		t.Fatalf("WithExpander error = %v, want ErrNilExpander", err)
	}
	if _, err := rag.WithRefiners(&fakeRetriever{}, nil); !errors.Is(err, rag.ErrNilRefiner) {
		t.Fatalf("WithRefiners error = %v, want ErrNilRefiner", err)
	}
	if _, err := rag.ReciprocalRankFusion(rag.FusionRetrieverConfig{}, &fakeRetriever{}, nil); !errors.Is(err, rag.ErrNilRetriever) {
		t.Fatalf("ReciprocalRankFusion error = %v, want ErrNilRetriever", err)
	}
}

func TestWithExpanderRejectsEmptyExpansion(t *testing.T) {
	expanded, err := rag.WithExpander(rag.ExpansionConfig{
		Retriever: &fakeRetriever{},
		Expander: rag.ExpanderFunc(func(context.Context, rag.Query) ([]rag.Query, error) {
			return nil, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = expanded.Retrieve(t.Context(), mustQuery(t, "hi"))
	if !errors.Is(err, rag.ErrEmptyExpansion) {
		t.Fatalf("Retrieve error = %v, want ErrEmptyExpansion", err)
	}
}

func TestWithExpanderValidatesAllQueriesBeforeRetrieval(t *testing.T) {
	retriever := &fakeRetriever{}
	expanded, err := rag.WithExpander(rag.ExpansionConfig{
		Retriever: retriever,
		Expander: rag.ExpanderFunc(func(context.Context, rag.Query) ([]rag.Query, error) {
			return []rag.Query{mustQuery(t, "valid"), {}}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := expanded.Retrieve(t.Context(), mustQuery(t, "source")); !errors.Is(err, rag.ErrInvalidExpansion) {
		t.Fatalf("invalid expansion error = %v", err)
	}
	if retriever.hits != 0 {
		t.Fatalf("retriever ran %d times before expansion validation completed", retriever.hits)
	}
}

func TestIdentityDefaults(t *testing.T) {
	q, _ := rag.NewQuery("hi")
	if got, _ := rag.IdentityAugmenter().Augment(t.Context(), q, nil); got.Text() != q.Text() {
		t.Fatal("Augment should preserve query text")
	}
}

func mustQuery(t *testing.T, text string) rag.Query {
	t.Helper()
	q, err := rag.NewQuery(text)
	if err != nil {
		t.Fatal(err)
	}
	return q
}
