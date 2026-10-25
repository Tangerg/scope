package chroma

import (
	"errors"
	"testing"

	v2 "github.com/amikos-tech/chroma-go/pkg/api/v2"

	"github.com/Tangerg/scope/core/vectorstore"
)

func TestUnfilteredSearchDoesNotEnumerateSource(t *testing.T) {
	config, collection, _ := boundaryConfig(t, v2.SpaceCosine)
	collection.getErr = errors.New("source reads denied")
	collection.query = queryRecords(oneRecord("null"), 0)
	store, err := NewStore(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{TopK: 1}})
	if err != nil || len(response.Results) != 1 || response.Results[0].Document.ID != "id" {
		t.Fatalf("native query = %v, %v", response, err)
	}
	if collection.gets != 0 || len(collection.queryOp.Ids) != 0 {
		t.Fatal("unfiltered query required source reads or native identity selection")
	}
}
