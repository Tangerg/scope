package milvus

import (
	"testing"

	"github.com/milvus-io/milvus/client/v3/entity"

	"github.com/Tangerg/scope/core/vectorstore"
)

func TestUnfilteredSearchDoesNotAuditUnreturnedRecords(t *testing.T) {
	store, service := nativeStore(t, entity.COSINE, nil)
	if service.queries != 0 {
		t.Fatal("construction queried stored rows")
	}
	service.records = []fixtureRecord{{id: "one", text: "text", metadata: "null", vector: []float32{1, 0}}, {id: "unrelated", text: "text", metadata: "broken", vector: []float32{0, 1}}}
	response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{TopK: 1}})
	if err != nil || len(response.Results) != 1 || response.Results[0].Document.ID != "one" {
		t.Fatalf("native query = %v, %v", response, err)
	}
	if service.queries != 0 || len(service.groups) != 1 || service.groups[0] != 0 {
		t.Fatal("unfiltered query enumerated rows or restricted identities")
	}
}
