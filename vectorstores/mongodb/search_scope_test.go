package mongodb

import (
	"errors"
	"testing"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/vectorstore"
)

func TestUnfilteredSearchDoesNotEnumerateSource(t *testing.T) {
	store, collection := mongoFilterStore(t, []*document.Document{{ID: "one", Text: "text"}})
	collection.enumerationErr = errors.New("source reads denied")
	response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{TopK: 1}})
	if err != nil || len(response.Results) != 1 || response.Results[0].Document.ID != "one" {
		t.Fatalf("native query = %v, %v", response, err)
	}
	if len(collection.queryBatches) != 0 {
		t.Fatal("unfiltered query restricted native identities")
	}
}
