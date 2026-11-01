package bedrockkb

import (
	"fmt"
	"testing"
)

func TestNativeDocumentIdentityDoesNotCollapseDistinctChunks(t *testing.T) {
	// Retrieve's documentId belongs to the source file; each content.text is a
	// chunk. These two results must survive consumers that fuse by Document.ID.
	page := `{"retrievalResults":[{"documentId":"source","content":{"type":"TEXT","text":"first chunk"},"score":0.9},{"documentId":"source","content":{"type":"TEXT","text":"second chunk"},"score":0.8}]}`
	response, err := newProtocolFixture(t, page).store().Retrieve(t.Context(), testQuery(t))
	if err != nil {
		t.Fatal(err)
	}

	if len(response) != 2 || response[0].Document.ID == response[1].Document.ID {
		t.Fatalf("chunks = %v", response)
	}
	for i, result := range response {
		if result.Document.ID == "" || string(result.Document.Metadata[SourceDocumentIDMetadataKey]) != `"source"` {
			t.Fatalf("chunk %d lost evidence identity or source provenance: %v", i, result.Document)
		}
	}
}

func TestEvidenceIdentityExcludesScoresAndOrdersNestedMetadata(t *testing.T) {
	var want string
	for _, facts := range []string{
		`{"a":{"x":9007199254740993,"y":1.0000000000000000001},"z":1e1000}`,
		`{ "z": 1e1000, "a": {"y": 1.0000000000000000001, "x": 9007199254740993} }`,
	} {
		for _, score := range []float64{-2, .8, 5} {
			page := fmt.Sprintf(`{"retrievalResults":[{"documentId":"source","content":{"type":"TEXT","text":"first chunk"},"score":%v,"metadata":%s}]}`, score, facts)
			response, err := newProtocolFixture(t, page).store().Retrieve(t.Context(), testQuery(t))
			if err != nil {
				t.Fatal(err)
			}
			if want == "" {
				want = response[0].Document.ID
			}
			if want == "" || response[0].Document.ID != want {
				t.Fatalf("query score or JSON key order changed evidence identity: %v, %v", response, err)
			}
		}
	}
}

func TestNativeMetadataCannotOverrideSourceProvenance(t *testing.T) {
	for _, key := range []string{SourceDocumentIDMetadataKey, SourceLocationMetadataKey} {
		page := fmt.Sprintf(`{"retrievalResults":[{"documentId":"source","content":{"type":"TEXT","text":"first chunk"},"score":0.9,"metadata":{%q:"forged"}}]}`, key)
		response, err := newProtocolFixture(t, page).store().Retrieve(t.Context(), testQuery(t))
		if err == nil || response != nil {
			t.Fatalf("reserved provenance collision accepted: %v, %v", response, err)
		}
	}
}
