package document_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/metadata"
)

func TestPhysicalMetadataRestoresTheExactBusinessViewAndOrigin(t *testing.T) {
	origin := document.NewProvenance("notes/guide", []byte("source bytes"))
	origin.Chunks = []document.Chunk{{Parent: "book", Index: 1, Total: 3}, {Parent: "part2", Index: 0, Total: 2}}
	origin.Page = 4
	origin.Bytes = &document.ByteRange{Start: 3, End: 7}
	for _, facts := range []metadata.Map{nil, {}, {"number": json.RawMessage("9007199254740993"), "null": json.RawMessage("null")}} {
		source := &document.Document{ID: "one", Text: "text", Metadata: facts, Provenance: origin}
		raw, err := document.MarshalStoredMetadata(source)
		if err != nil {
			t.Fatal(err)
		}
		if _, leaked := facts[document.StoredProvenanceKey]; leaked {
			t.Fatal("serialization modified owned metadata")
		}
		copy := &document.Document{ID: "original", Text: "unchanged"}
		if err = document.UnmarshalStoredMetadata(raw, copy); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(copy.Metadata, facts) || !reflect.DeepEqual(copy.Provenance, origin) || copy.ID != "original" || copy.Text != "unchanged" {
			t.Fatalf("logical projection changed: %#v", copy)
		}
		copy.Provenance.Chunks[0].Parent = "changed"
		if origin.Chunks[0].Parent != "book" {
			t.Fatal("origin aliased source")
		}
		plain := &document.Document{ID: "one", Text: "text", Metadata: facts}
		raw, err = document.MarshalStoredMetadata(plain)
		if err != nil {
			t.Fatal(err)
		}
		if err = document.UnmarshalStoredMetadata(raw, copy); err != nil || copy.Provenance != nil || !reflect.DeepEqual(copy.Metadata, facts) {
			t.Fatal("absent origin survived replacing a cell", err)
		}
	}
}

func TestMalformedPhysicalMetadataCannotReplaceTheDocumentOrForgeProvenance(t *testing.T) {
	key := document.StoredProvenanceKey
	original := &document.Document{ID: "doc", Text: "evidence", Metadata: metadata.Map{"visible": json.RawMessage("true")}, Provenance: document.NewProvenance("file", []byte("source"))}
	for _, cell := range []string{"null", "{}", `{"provenance":{} }`, `{"provenance":{"source":"s"}}`, `{"provenance":{"page":2},"unknown":true}`, `{"provenance":{"page":2},"null_metadata":true}`} {
		dst := original.Clone()
		raw := []byte(`{"ordinary":42,"` + key + `":` + cell + `}`)
		if err := document.UnmarshalStoredMetadata(raw, dst); !errors.Is(err, document.ErrInvalidDocument) || !reflect.DeepEqual(dst, original) {
			t.Fatalf("partial or fabricated origin: %s %#v %v", raw, dst, err)
		}
	}
	collision := original.Clone()
	collision.Metadata[key] = json.RawMessage(`{"provenance":{"page":9}}`)
	if _, err := document.MarshalStoredMetadata(collision); !errors.Is(err, document.ErrInvalidDocument) {
		t.Fatalf("reserved user key entered native storage: %v", err)
	}
}
