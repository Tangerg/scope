package redis

import (
	"testing"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/vectorstore"
)

func TestUnfilteredSearchDoesNotAuditUnreturnedHashes(t *testing.T) {
	fixture := newProtocolFixture(t)
	fixture.rows["embedding:unrelated"] = map[string]string{"unknown": "broken"}
	fixture.distances["embedding:unrelated"] = "2"
	store := fixture.store(constantModel(), 32)
	if err := store.Index(t.Context(), &vectorstore.IndexRequest{Documents: []*document.Document{{ID: "one", Text: "text"}}}); err != nil {
		t.Fatal(err)
	}
	response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{TopK: 1}})
	if err != nil || len(response.Results) != 1 || response.Results[0].Document.ID != "one" {
		t.Fatalf("native query = %v, %v", response, err)
	}
	if len(fixture.queries) != 1 || fixture.queries[0] != 0 {
		t.Fatal("unfiltered query restricted native identities")
	}
}
