package clickhouse

import (
	"testing"

	"github.com/Tangerg/scope/core/metadata"
)

// Metadata is stored as JSON text, so encoding and decoding are inverses and a
// document reads back as it was written. Stringifying the decoded scalars
// instead cost the type: year 2020 came back as the string "2020", so
// Decode[int] failed on a document this store had accepted, and a nil value
// became "" — the same text as an empty string, which the AST reads as a
// different value.
func TestMetadataRoundTripsExactly(t *testing.T) {
	t.Parallel()

	source := metadata.Map{}
	values := map[string]any{
		"year":   int64(2020),
		"score":  0.5,
		"active": true,
		"author": "Alice",
		"empty":  "",
		"note":   nil,
	}
	for key, value := range values {
		if err := source.Set(key, value); err != nil {
			t.Fatal(err)
		}
	}

	encoded, err := metadataAsStringMap(source)
	if err != nil {
		t.Fatalf("metadataAsStringMap() = %v, want nil", err)
	}
	// A string value carries its JSON quotes, which is what makes it
	// distinguishable from a number that happens to share its digits.
	if encoded["author"] != `"Alice"` || encoded["year"] != "2020" || encoded["note"] != "null" {
		t.Fatalf("encoded = %#v", encoded)
	}

	decoded, err := stringMapToMetadata(encoded)
	if err != nil {
		t.Fatalf("stringMapToMetadata() = %v, want nil", err)
	}
	if !decoded.Equal(source) {
		t.Fatalf("decoded = %#v, want %#v", decoded, source)
	}

	roundTripped, err := decoded.Values()
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range values {
		if roundTripped[key] != want {
			t.Fatalf("values[%q] = %#v (%T), want %#v (%T)",
				key, roundTripped[key], roundTripped[key], want, want)
		}
	}
}

// An empty map stays empty rather than becoming a map with no entries, so a
// document without metadata reads back without metadata.
func TestMetadataRoundTripsEmpty(t *testing.T) {
	t.Parallel()

	encoded, err := metadataAsStringMap(nil)
	if err != nil {
		t.Fatalf("metadataAsStringMap(nil) = %v, want nil", err)
	}
	if len(encoded) != 0 {
		t.Fatalf("encoded = %#v, want empty", encoded)
	}
	decoded, err := stringMapToMetadata(encoded)
	if err != nil {
		t.Fatalf("stringMapToMetadata() = %v, want nil", err)
	}
	if !decoded.IsZero() {
		t.Fatalf("decoded = %#v, want zero", decoded)
	}
}

// Text that is not JSON cannot have come from this store's write path, so the
// read path reports it instead of handing a caller a Map whose values fail to
// decode at the next hop.
func TestMetadataDecodeRejectsNonJSON(t *testing.T) {
	t.Parallel()

	if _, err := stringMapToMetadata(map[string]string{"author": "Alice"}); err == nil {
		t.Fatal("stringMapToMetadata(bare text) = nil error, want a decode error")
	}
}
