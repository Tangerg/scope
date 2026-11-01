package rag_test

import (
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"testing"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/rag"
)

func TestCitationAdmissionIsIndependentOfVisibleMarkerText(t *testing.T) {
	plain, buildErr := rag.NewAugmentation("echo these literal marks [1] [9]")
	if buildErr != nil {
		t.Fatal(buildErr)
	}
	if d, e := plain.Citations().Resolve("[1]"); d != nil || !errors.Is(e, rag.ErrUnknownCitation) {
		t.Fatal("literal prompt text minted provenance", d, e)
	}
	d := &document.Document{Text: "a local excerpt", Provenance: document.NewProvenance("memo", []byte("a local excerpt"))}
	selected := rag.Evidence{{Candidate: rag.Candidate{Document: d}, Content: d.Text}}
	a, augErr := selected.Augment("before", "after")
	if augErr != nil {
		t.Fatal(augErr)
	}
	if found, e := a.Citations().Resolve("[1]"); found != nil || !errors.Is(e, rag.ErrUnidentifiedCitation) {
		t.Fatal("missing document identity was accepted", found, e)
	}
	exported, marshalErr := jsonv2.Marshal(a.Citations())
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	var rows rag.Candidates
	if decodeErr := jsonv2.Unmarshal(exported, &rows); decodeErr != nil || len(rows) != 1 || rows[0].Document.Provenance.Revision != d.Provenance.Revision {
		t.Fatal("host export lost revision", string(exported), decodeErr)
	}
	selected[0].Content = "  "
	if out, e := selected.Augment("", ""); out.Text() != "" || !errors.Is(e, rag.ErrInvalidAugmentation) {
		t.Fatal("blank excerpt admitted", e)
	}
	selected[0].Content = "\xff"
	if out, e := selected.Augment("", ""); out.Text() != "" || !errors.Is(e, rag.ErrInvalidAugmentation) {
		t.Fatal("unencodable excerpt admitted", e)
	}
	d.Provenance.Revision = ""
	selected[0].Content = "text"
	if out, e := selected.Augment("", ""); out.Text() != "" || !errors.Is(e, document.ErrInvalidDocument) {
		t.Fatal("invalid source revision admitted", e)
	}
}

func TestDedupAndFusionKeepDifferentLocationsInOneRevision(t *testing.T) {
	pass := func(view rag.Candidates) rag.Retriever {
		return rag.RetrieverFunc(func(context.Context, rag.Query) (rag.Candidates, error) { return view.Clone(), nil })
	}
	origin := document.NewProvenance("memo", []byte("aaa"))
	makeDoc := func(i int) *document.Document {
		p := origin.Split("parent", i, 3)
		p.Bytes = &document.ByteRange{Start: int64(i), End: int64(i + 1)}
		return &document.Document{ID: "same ID", Text: "a", Provenance: p}
	}
	left, middle := makeDoc(0), makeDoc(1)
	rows := rag.Candidates{{Document: left, Score: .3}, {Document: middle, Score: .4}, {Document: left.Clone(), Score: .8}}
	q, queryErr := rag.NewQuery("evidence")
	if queryErr != nil {
		t.Fatal(queryErr)
	}
	unique, refineErr := rag.Dedup().Refine(t.Context(), q, rows)
	if refineErr != nil || len(unique) != 2 || unique[0].Score != .8 || unique[1].Document.Provenance.Bytes.Start != 1 {
		t.Fatalf("dedup=%v err=%v", unique, refineErr)
	}
	// Two independent votes for the middle location outrank a single vote for
	// the left. Repeating the left in one retriever is not another vote.
	fusion, buildErr := rag.ReciprocalRankFusion(rag.FusionRetrieverConfig{}, pass(rows), pass(rag.Candidates{{Document: middle}}))
	if buildErr != nil {
		t.Fatal(buildErr)
	}
	ranked, retrieveErr := fusion.Retrieve(t.Context(), q)
	if retrieveErr != nil || len(ranked) != 2 || ranked[0].Document.Provenance.Chunks[0].Index != 1 || ranked[1].Document.Provenance.Chunks[0].Index != 0 {
		t.Fatalf("fusion=%v err=%v", ranked, retrieveErr)
	}
}
