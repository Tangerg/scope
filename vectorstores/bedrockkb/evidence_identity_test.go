package bedrockkb

import (
	"fmt"
	"testing"

	"github.com/Tangerg/scope/core/vectorstore"
)

func TestNativeDocumentIdentityDoesNotCollapseDistinctChunks(t *testing.T) {
	// Retrieve's documentId belongs to the source file; each content.text is a
	// chunk. These two results must survive consumers that fuse by Document.ID.
	page := `{"retrievalResults":[{"documentId":"source","content":{"type":"TEXT","text":"first chunk"},"score":0.9},{"documentId":"source","content":{"type":"TEXT","text":"second chunk"},"score":0.8}]}`
	response, err := newProtocolFixture(t, page).store().Search(t.Context(), &vectorstore.SearchRequest{Query: "query"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"bedrockkb:08926c1cadd929c83b6369c0b8ff788a1de5d84e69a26bb8127cda97d2e13b34",
		"bedrockkb:5e009cebf4e3102b384e279c7ebcfb5037f09edde96ca3f0863fd0108e64eb4d",
	}
	if len(response.Results) != len(want) {
		t.Fatalf("chunks = %v", response.Results)
	}
	for i, result := range response.Results {
		if result.Document.ID != want[i] || string(result.Document.Metadata[SourceDocumentIDMetadataKey]) != `"source"` {
			t.Fatalf("chunk %d lost evidence identity or source provenance: %v", i, result.Document)
		}
	}
}

func TestEvidenceIdentityExcludesScoresAndOrdersNestedMetadata(t *testing.T) {
	want := "bedrockkb:f33daac55e565a33545c04825d31c3f46e4cf759d4a1589bf4753b37a4741cb2"
	for _, facts := range []string{
		`{"a":{"x":9007199254740993,"y":1.0000000000000000001},"z":1e1000}`,
		`{ "z": 1e1000, "a": {"y": 1.0000000000000000001, "x": 9007199254740993} }`,
	} {
		for _, score := range []float64{-2, .8, 5} {
			page := fmt.Sprintf(`{"retrievalResults":[{"documentId":"source","content":{"type":"TEXT","text":"first chunk"},"score":%v,"metadata":%s}]}`, score, facts)
			response, err := newProtocolFixture(t, page).store().Search(t.Context(), &vectorstore.SearchRequest{Query: "query"})
			if err != nil || response.Results[0].Document.ID != want {
				t.Fatalf("query score or JSON key order changed evidence identity: %v, %v", response, err)
			}
		}
	}
}

func TestNativeMetadataCannotOverrideSourceProvenance(t *testing.T) {
	for _, key := range []string{SourceDocumentIDMetadataKey, SourceLocationMetadataKey} {
		page := fmt.Sprintf(`{"retrievalResults":[{"documentId":"source","content":{"type":"TEXT","text":"first chunk"},"score":0.9,"metadata":{%q:"forged"}}]}`, key)
		response, err := newProtocolFixture(t, page).store().Search(t.Context(), &vectorstore.SearchRequest{Query: "query"})
		if err == nil || response != nil {
			t.Fatalf("reserved provenance collision accepted: %v, %v", response, err)
		}
	}
}
