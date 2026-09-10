package neo4j

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/metadata"
)

func TestStoreConfigRejectsPropertyCollisions(t *testing.T) {
	config := StoreConfig{IDProperty: "value", TextProperty: "value"}
	config.applyDefaults()
	if err := config.validateIdentifiers(); err == nil {
		t.Fatal("validateIdentifiers accepted colliding document properties")
	}
}

func TestQuoteIdentifierEscapesBackticks(t *testing.T) {
	if got, want := quoteIdentifier("index`name"), "`index``name`"; got != want {
		t.Fatalf("quoteIdentifier = %q, want %q", got, want)
	}
}

func TestDocumentPropertiesDefinesCompleteOwnedNode(t *testing.T) {
	values, err := metadata.FromValues(map[string]any{"tenant": "acme"})
	if err != nil {
		t.Fatal(err)
	}
	store := &Store{idProperty: "id", textProperty: "text", metadataPrefix: "metadata"}
	properties, err := store.documentProperties(&document.Document{ID: "doc-1", Text: "hello", Metadata: values})
	if err != nil {
		t.Fatalf("documentProperties: %v", err)
	}
	want := map[string]any{"id": "doc-1", "text": "hello", "metadata.tenant": "acme"}
	if len(properties) != len(want) {
		t.Fatalf("properties = %#v", properties)
	}
	for key, value := range want {
		if properties[key] != value {
			t.Fatalf("properties[%q] = %#v, want %#v", key, properties[key], value)
		}
	}
}

func TestMetadataValuesSelectOnlyOwnedProperties(t *testing.T) {
	store := &Store{metadataPrefix: "metadata"}
	values := store.metadataValues(map[string]any{
		"id":              "doc-1",
		"metadata.tenant": "acme",
		"foreign":         "ignored",
	})
	if len(values) != 1 || values["tenant"] != "acme" {
		t.Fatalf("metadataValues = %#v", values)
	}
}

func TestDocumentPropertiesPreserveNumericValues(t *testing.T) {
	store := &Store{idProperty: "id", textProperty: "text", metadataPrefix: "metadata"}
	properties, err := store.documentProperties(&document.Document{ID: "one", Text: "text", Metadata: metadata.Map{
		"large": json.RawMessage(`9007199254740993.0`),
		"items": json.RawMessage(`[9.007199254740993e15,0.25]`),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if properties["metadata.large"] != int64(9007199254740993) || !reflect.DeepEqual(properties["metadata.items"], []any{int64(9007199254740993), 0.25}) {
		t.Fatalf("properties = %#v", properties)
	}
	for _, number := range []string{"1e1000", "1.00000000000000001"} {
		_, err := store.documentProperties(&document.Document{ID: "one", Text: "text", Metadata: metadata.Map{"number": json.RawMessage(number)}})
		if err == nil {
			t.Fatalf("accepted unrepresentable number %s", number)
		}
	}
}
