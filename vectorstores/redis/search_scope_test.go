package redis

import (
	"testing"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/vectorstore"
)

func TestUnfilteredSearchDoesNotAuditUnreturnedHashes(t *testing.T) {
	fixture := newProtocolFixture(t)
	fixture.rows[fixturePrefix+"unrelated"] = map[string]string{"unknown": "broken"}
	fixture.distances[fixturePrefix+"unrelated"] = "2"
	store := fixture.store(32)
	if err := store.Upsert(t.Context(), fixtureBatch([]*document.Document{{ID: "one", Text: "text"}})); err != nil {
		t.Fatal(err)
	}
	response, err := fixtureQuery(t.Context(), store, &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("query"), Options: vectorstore.SearchOptions{TopK: 1}})
	if err != nil || len(response.Results) != 1 || response.Results[0].Document.ID != "one" {
		t.Fatalf("native query = %v, %v", response, err)
	}
	if len(fixture.queries) != 1 || fixture.queries[0] != 0 {
		t.Fatal("unfiltered query restricted native identities")
	}
}
