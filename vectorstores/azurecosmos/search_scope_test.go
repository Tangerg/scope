package azurecosmos

import (
	"net/http"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/data/azcosmos"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/vectorstore"
)

func TestUnfilteredSearchDoesNotEnumerateSource(t *testing.T) {
	fixture := newProtocolFixture(t, azcosmos.VectorDistanceFunctionCosine)
	fixture.records["one"] = storedDocument(t, &document.Document{ID: "one", Text: "text"})
	fixture.scanScript = []queryPage{{status: http.StatusForbidden}}
	store := fixture.store(constantModel(), 32)
	response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{TopK: 1}})
	if err != nil || len(response.Results) != 1 || response.Results[0].Document.ID != "one" {
		t.Fatalf("native query = %v, %v", response, err)
	}
	if len(fixture.scanScript) != 1 {
		t.Fatal("unfiltered operation consumed source pages")
	}
}
