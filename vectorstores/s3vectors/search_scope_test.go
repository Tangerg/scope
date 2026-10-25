package s3vectors

import (
	"net/http"
	"slices"
	"testing"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/vectorstore"
)

func TestUnfilteredSearchDoesNotRequireListPermission(t *testing.T) {
	fixture := newProtocolFixture(t, &document.Document{ID: "one", Text: "text"})
	fixture.status["/ListVectors"] = http.StatusForbidden
	store := fixture.store()
	response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{TopK: 1}})
	if err != nil || len(response.Results) != 1 || response.Results[0].Document.ID != "one" {
		t.Fatalf("native query = %v, %v", response, err)
	}
	if slices.Contains(fixture.paths, "/ListVectors") {
		t.Fatal("unfiltered operation required vector listing")
	}
}
