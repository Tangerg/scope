package weaviate

import (
	"testing"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/vectorstore"
)

func TestUnfilteredSearchDoesNotEnumerateSource(t *testing.T) {
	const id = "00000000-0000-0000-0000-000000000001"
	store, fixture := indexedNativeStore(t, []*document.Document{{ID: id, Text: "text"}})
	scans := fixture.scans
	fixture.sourceError = true
	response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{TopK: 1}})
	if err != nil || len(response.Results) != 1 || response.Results[0].Document.ID != id {
		t.Fatalf("native query = %v, %v", response, err)
	}
	if fixture.scans != scans {
		t.Fatal("unfiltered query enumerated objects")
	}
}
