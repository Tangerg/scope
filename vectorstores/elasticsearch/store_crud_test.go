package elasticsearch

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/metadata"
)

func TestToDocumentAcceptsIndexedNilMetadata(t *testing.T) {
	store := &Store{contentField: "content", metadataField: "metadata"}
	encoded, err := json.Marshal(map[string]any{"content": "hello", "metadata": metadata.Map(nil)})
	if err != nil {
		t.Fatal(err)
	}
	var source map[string]any
	if decodeErr := json.Unmarshal(encoded, &source); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	document, err := store.toDocument(searchHit{ID: "doc-1", Source: source})
	if err != nil {
		t.Fatal(err)
	}
	if document.ID != "doc-1" || document.Text != "hello" || document.Metadata != nil {
		t.Fatalf("document = %+v", document)
	}
}

func TestToDocumentDecodesConfiguredMetadataObject(t *testing.T) {
	store := &Store{contentField: "content", embeddingField: "embedding", metadataField: "metadata"}
	document, err := store.toDocument(searchHit{
		ID: "doc-1",
		Source: map[string]any{
			"content":   "hello",
			"embedding": []any{0.1, 0.2},
			"metadata":  map[string]any{"tenant": "acme"},
		},
	})
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

func TestToDocumentRejectsMalformedConfiguredMetadata(t *testing.T) {
	store := &Store{contentField: "content", metadataField: "metadata"}
	_, err := store.toDocument(searchHit{
		ID:     "doc-1",
		Source: map[string]any{"content": "hello", "metadata": "not-an-object"},
	})
	if err == nil || !strings.Contains(err.Error(), `field "metadata" must be an object`) {
		t.Fatalf("toDocument error = %v", err)
	}
}

func TestToDocumentExtractsFlattenedMetadata(t *testing.T) {
	store := &Store{contentField: "content", embeddingField: "embedding"}
	document, err := store.toDocument(searchHit{
		ID: "doc-1",
		Source: map[string]any{
			"content":   "hello",
			"embedding": []any{0.1, 0.2},
			"tenant":    "acme",
		},
	})
	if err != nil {
		t.Fatalf("toDocument: %v", err)
	}
	values, err := document.Metadata.Values()
	if err != nil {
		t.Fatalf("metadata Values: %v", err)
	}
	if len(values) != 1 || values["tenant"] != "acme" {
		t.Fatalf("metadata = %#v", values)
	}
}
