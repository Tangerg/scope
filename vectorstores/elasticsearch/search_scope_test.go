package elasticsearch

import (
	"testing"

	"github.com/Tangerg/scope/core/vectorstore"
)

func TestUnfilteredSearchDoesNotAuditUnreturnedRecords(t *testing.T) {
	fixture := newProtocolFixture(t)
	fixture.rows["one"] = storedDocument{Content: "text", MetadataJSON: "null", Embedding: []float32{1, 0}}
	fixture.rows["unrelated"] = storedDocument{Content: "text", MetadataJSON: "broken", Embedding: []float32{0, 1}}
	fixture.scores["one"], fixture.scores["unrelated"] = 1, 0
	store := fixture.store(constantModel(), 32)
	response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{TopK: 1}})
	if err != nil || len(response.Results) != 1 || response.Results[0].Document.ID != "one" {
		t.Fatalf("native query = %v, %v", response, err)
	}
	if fixture.cleared != 0 || len(fixture.groups) != 1 || fixture.groups[0] != 0 {
		t.Fatal("unfiltered query opened a source cursor or restricted identities")
	}
}
