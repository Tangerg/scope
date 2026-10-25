package vectara

import (
	"testing"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/vectorstore"
)

func TestUnfilteredSearchDoesNotListOrRequestGeneration(t *testing.T) {
	store, fixture := indexedNativeStore(t, []*document.Document{{ID: "one", Text: "text"}})
	fixture.pages = []string{`{"documents":null}`}
	fixture.listed = 0
	response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{TopK: 1}})
	if err != nil || len(response.Results) != 1 || response.Results[0].Document.ID != "one" {
		t.Fatalf("native query = %v, %v", response, err)
	}
	if fixture.listed != 0 {
		t.Fatal("unfiltered query listed the corpus")
	}
	// The wire fixture rejects any generation member: Vectara enables generation
	// whenever that object is present, including an unrecognized enabled:false.
}
