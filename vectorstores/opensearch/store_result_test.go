package opensearch

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"strings"
	"testing"

	"github.com/opensearch-project/opensearch-go/v4/opensearchapi"

	"github.com/Tangerg/scope/core/metadata"
)

func TestToDocumentAcceptsIndexedNilMetadata(t *testing.T) {
	store := &Store{contentField: "content", metadataField: "metadata"}
	source, err := jsonv2.Marshal(map[string]any{"content": "hello", "metadata": metadata.Map(nil)})
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
	source, err := jsonv2.Marshal(map[string]any{
		"content":  "hello",
		"":         "unrelated source field",
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
	semantic, ok := errors.AsType[*jsonv2.SemanticError](err)
	if !ok || semantic.JSONKind != '"' || !strings.Contains(err.Error(), `field "metadata"`) {
		t.Fatalf("toDocument error = %v", err)
	}
}

func TestToDocumentPreservesExactJSONNumbers(t *testing.T) {
	store := &Store{contentField: "content", metadataField: "metadata"}
	for _, value := range []string{`9007199254740993`, `18446744073709551615`, `0.1`, `1.25e-3`, `{"nested":9007199254740993}`} {
		doc, err := store.toDocument(opensearchapi.SearchHit{ID: "one", Source: json.RawMessage(`{"content":"hello","metadata":{"value":` + value + `}}`)})
		if err != nil {
			t.Fatal(err)
		}
		if string(doc.Metadata["value"]) != value {
			t.Fatalf("got %s, want %s", doc.Metadata["value"], value)
		}
	}
}
