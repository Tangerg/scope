package azureaisearch

import (
	"net/http"
	"testing"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
)

func TestUnfilteredSearchDoesNotEnumerateSource(t *testing.T) {
	fixture := newProtocolFixture(t)
	fixture.put(&document.Document{ID: "one", Text: "text"})
	fixture.scan = func(w http.ResponseWriter, _ metadata.Map) {
		t.Error("unfiltered operation enumerated source")
		w.WriteHeader(http.StatusForbidden)
	}
	store := fixture.store()
	response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{TopK: 1}})
	if err != nil || len(response.Results) != 1 || response.Results[0].Document.ID != "one" {
		t.Fatalf("native query = %v, %v", response, err)
	}
	if len(fixture.bodies) != 1 {
		t.Fatalf("data requests = %v", fixture.bodies)
	}
	if _, present := fixture.bodies[0]["filter"]; present {
		t.Fatal("unfiltered query restricted native identities")
	}
}
