package opensearch

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/opensearch-project/opensearch-go/v4/opensearchapi"

	"github.com/Tangerg/scope/core/metadata"
)

func TestToDocumentAcceptsIndexedNilMetadata(t *testing.T) {
	store := &Store{contentField: "content", metadataField: "metadata"}
	source, err := json.Marshal(map[string]any{"content": "hello", "metadata": metadata.Map(nil)})
	if err != nil {
		t.Fatal(err)
	}
	document, err := store.toDocument(opensearchapi.SearchHit{ID: "doc-1", Source: source})
	if err != nil {
		t.Fatal(err)
	}
	if document.ID != "doc-1" || document.Text != "hello" || document.Metadata != nil {
		t.Fatalf("document = %+v", document)
	}
}

func TestStoreConfigRejectsFieldCollisions(t *testing.T) {
	config := StoreConfig{ContentField: "payload", EmbeddingField: "payload"}
	config.applyDefaults()
	if err := config.validateFieldLayout(); err == nil || !strings.Contains(err.Error(), "both use field") {
		t.Fatalf("validateFieldLayout error = %v", err)
	}
}

func TestToDocumentDecodesOwnedMetadata(t *testing.T) {
	store := &Store{contentField: "content", embeddingField: "embedding", metadataField: "metadata"}
	source, err := json.Marshal(map[string]any{
		"content":  "hello",
		"metadata": map[string]any{"tenant": "acme"},
	})
	if err != nil {
		t.Fatal(err)
	}
	document, err := store.toDocument(opensearchapi.SearchHit{ID: "doc-1", Source: source})
	if err != nil {
		t.Fatalf("toDocument: %v", err)
	}
	values, err := document.Metadata.Values()
	if err != nil {
		t.Fatalf("metadata Values: %v", err)
	}
	if got := values["tenant"]; got != "acme" {
		t.Fatalf("tenant = %#v", got)
	}
}

func TestToDocumentRejectsMalformedOwnedMetadata(t *testing.T) {
	store := &Store{contentField: "content", embeddingField: "embedding", metadataField: "metadata"}
	source := json.RawMessage(`{"content":"hello","metadata":"not-an-object"}`)
	_, err := store.toDocument(opensearchapi.SearchHit{ID: "doc-1", Source: source})
	if err == nil || !strings.Contains(err.Error(), `field "metadata" must be an object`) {
		t.Fatalf("toDocument error = %v", err)
	}
}
