package redis

import (
	"encoding/json"
	"slices"
	"testing"

	goredis "github.com/redis/go-redis/v9"

	"github.com/Tangerg/scope/core/metadata"
)

// A document reads back with the metadata it was written with. Reconstructing
// metadata from the declared index fields could not do that: RediSearch indexes
// a HASH field's text as its declared type, so a NUMERIC field came back as a
// float64, every other field as a string, and a key with no declared field came
// back not at all.
func TestSearchResultMetadataRoundTripsExactly(t *testing.T) {
	t.Parallel()

	source := metadata.Map{}
	values := map[string]any{
		"year":   int64(2020),
		"score":  0.5,
		"active": true,
		"author": "Alice",
		"note":   nil,
		// Declared fields are the filterable subset; an undeclared key is still
		// part of the document.
		"undeclared": "kept",
	}
	for key, value := range values {
		if err := source.Set(key, value); err != nil {
			t.Fatal(err)
		}
	}
	encoded, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}

	store := &Store{
		keyPrefix:         "embedding:",
		contentField:      "content",
		metadataJSONField: "metadata_json",
		metadataFields:    []MetadataField{{Name: "year", Type: FieldNumeric}},
	}
	doc, err := store.toDocument(goredis.Document{
		ID: "embedding:one",
		Fields: map[string]string{
			"content":       "body",
			"metadata_json": string(encoded),
			// The projection is still written for the index; it must not be
			// what a result reads.
			"year": "2020",
		},
	})
	if err != nil {
		t.Fatalf("toDocument() = %v, want nil", err)
	}
	if doc.ID != "one" || doc.Text != "body" {
		t.Fatalf("document = %#v", doc)
	}

	roundTripped, err := doc.Metadata.Values()
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range values {
		if roundTripped[key] != want {
			t.Fatalf("metadata[%q] = %#v (%T), want %#v (%T)",
				key, roundTripped[key], roundTripped[key], want, want)
		}
	}
}

// Text that is not JSON cannot have come from this store's write path, so the
// read path reports it instead of handing back a document with silently empty
// metadata.
func TestSearchResultRejectsNonJSONMetadata(t *testing.T) {
	t.Parallel()

	store := &Store{keyPrefix: "embedding:", contentField: "content", metadataJSONField: "metadata_json"}
	_, err := store.toDocument(goredis.Document{
		ID:     "embedding:one",
		Fields: map[string]string{"content": "body", "metadata_json": "not json"},
	})
	if err == nil {
		t.Fatal("toDocument(non-JSON metadata) = nil error, want a decode error")
	}
}

// The search asks for the metadata field by name. RediSearch would otherwise
// omit it: RETURN limits the reply to the fields it lists, so a metadata field
// missing from that list reads back as an absent field rather than as an error.
func TestSearchRequestsTheMetadataField(t *testing.T) {
	t.Parallel()

	store := &Store{
		contentField:      "content",
		metadataJSONField: "metadata_json",
		metadataFields:    []MetadataField{{Name: "year", Type: FieldNumeric}},
	}
	names := make([]string, 0, 3)
	for _, field := range store.returnFields() {
		names = append(names, field.FieldName)
	}
	want := []string{"content", distanceFieldName, "metadata_json"}
	if !slices.Equal(names, want) {
		t.Fatalf("returnFields() = %v, want %v", names, want)
	}
}
