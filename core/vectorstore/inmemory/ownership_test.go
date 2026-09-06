package inmemory_test

import (
	"fmt"
	"testing"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/vectorstore"
)

func TestIndexSnapshot(t *testing.T) {
	s := newStore(t)
	d := &document.Document{ID: "one", Text: "original"}
	if err := s.Index(t.Context(), &vectorstore.IndexRequest{Documents: []*document.Document{d}}); err != nil {
		t.Fatal(err)
	}
	d.Text = "changed by caller"
	r, err := s.Search(t.Context(), &vectorstore.SearchRequest{Query: "original"})
	if err != nil {
		t.Fatal(err)
	}
	if r.Results[0].Document.Text != "original" {
		t.Fatalf("stored text changed without Index: %q", r.Results[0].Document.Text)
	}
}
func TestSearchSnapshot(t *testing.T) {
	s := newStore(t)
	d := &document.Document{ID: "one", Text: "original"}
	if err := s.Index(t.Context(), &vectorstore.IndexRequest{Documents: []*document.Document{d}}); err != nil {
		t.Fatal(err)
	}
	q := &vectorstore.SearchRequest{Query: "original"}
	r, err := s.Search(t.Context(), q)
	if err != nil {
		t.Fatal(err)
	}
	r.Results[0].Document.Text = "changed through search result"
	next, err := s.Search(t.Context(), q)
	if err != nil {
		t.Fatal(err)
	}
	if next.Results[0].Document.Text != "original" {
		t.Fatalf("stored text changed through result: %q", next.Results[0].Document.Text)
	}
}
func TestStableTies(t *testing.T) {
	s := newStore(t)
	ds := make([]*document.Document, 16)
	for i := range ds {
		ds[i] = &document.Document{ID: fmt.Sprintf("doc-%02d", i), Text: "same"}
	}
	if err := s.Index(t.Context(), &vectorstore.IndexRequest{Documents: ds}); err != nil {
		t.Fatal(err)
	}
	q := &vectorstore.SearchRequest{Query: "same", Options: vectorstore.SearchOptions{TopK: 1}}
	for range 20 {
		r, err := s.Search(t.Context(), q)
		if err != nil {
			t.Fatal(err)
		}
		if id := r.Results[0].Document.ID; id != "doc-00" {
			t.Fatalf("TopK ID=%q, want doc-00", id)
		}
	}
}
