package document_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/core/metadata"
)

func TestDocumentValidate(t *testing.T) {
	if err := (*document.Document)(nil).Validate(); err == nil {
		t.Fatal("nil document must fail validation")
	}
	if err := (&document.Document{}).Validate(); err == nil {
		t.Fatal("document without text or media must fail validation")
	}
	if err := (&document.Document{Text: "hello"}).Validate(); err != nil {
		t.Fatalf("text document: %v", err)
	}
	if err := (&document.Document{Text: "\xff"}).Validate(); !errors.Is(err, document.ErrInvalidDocument) {
		t.Fatalf("invalid UTF-8 error = %v, want ErrInvalidDocument", err)
	}
}

func TestDocumentClone(t *testing.T) {
	inline, err := media.NewBytes("image/png", []byte{1, 2, 3})
	if err != nil {
		t.Fatal(err)
	}
	original := &document.Document{ID: "doc-1", Media: inline, Metadata: metadata.Map{}}
	if err := original.Metadata.Set("source", "original"); err != nil {
		t.Fatal(err)
	}

	clone := original.Clone()
	clone.Media.Source.Bytes[0] = 9
	clone.Metadata["source"][1] = 'X'

	if original.Media.Source.Bytes[0] != 1 {
		t.Fatal("clone shares media bytes with original")
	}
	if got := string(original.Metadata["source"]); got != `"original"` {
		t.Fatalf("clone shares metadata with original: %s", got)
	}
	if (*document.Document)(nil).Clone() != nil {
		t.Fatal("nil document clone must remain nil")
	}
}

func TestNewDocumentReturnsNilOnInvalidMedia(t *testing.T) {
	doc, err := document.NewDocument("", &media.Media{})
	if err == nil || doc != nil {
		t.Fatalf("NewDocument() = (%#v, %v), want (nil, error)", doc, err)
	}
}

func TestDocumentContainsOnlyDataFields(t *testing.T) {
	typ := reflect.TypeFor[document.Document]()
	want := []string{"ID", "Text", "Media", "Metadata"}
	if typ.NumField() != len(want) {
		t.Fatalf("Document fields = %d, want %d", typ.NumField(), len(want))
	}
	for i, name := range want {
		if got := typ.Field(i).Name; got != name {
			t.Fatalf("field %d = %s, want %s", i, got, name)
		}
	}
}

func TestDocumentJSONRoundTrip(t *testing.T) {
	original := document.Document{ID: "doc-1", Text: "hello", Metadata: metadata.Map{}}
	if err := original.Metadata.Set("source", "test"); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	var decoded document.Document
	if unmarshalErr := json.Unmarshal(data, &decoded); unmarshalErr != nil {
		t.Fatal(unmarshalErr)
	}
	source, ok, err := decoded.Metadata.Decode[string]("source")
	if decoded.ID != original.ID || decoded.Text != original.Text || err != nil || !ok || source != "test" {
		t.Fatalf("round trip = %#v", decoded)
	}
}

func TestDocumentJSONRejectsInvalidValuesTransactionally(t *testing.T) {
	if _, err := json.Marshal(document.Document{}); !errors.Is(err, document.ErrInvalidDocument) {
		t.Fatalf("Marshal error = %v, want ErrInvalidDocument", err)
	}

	original := document.Document{ID: "stable", Text: "original"}
	decoded := original
	if err := json.Unmarshal([]byte(`{"id":"replacement"}`), &decoded); !errors.Is(err, document.ErrInvalidDocument) {
		t.Fatalf("Unmarshal error = %v, want ErrInvalidDocument", err)
	}
	if !reflect.DeepEqual(decoded, original) {
		t.Fatalf("failed Unmarshal mutated receiver: got %#v, want %#v", decoded, original)
	}

	var nilDocument *document.Document
	if err := nilDocument.UnmarshalJSON([]byte(`{"text":"value"}`)); !errors.Is(err, document.ErrInvalidDocument) {
		t.Fatalf("nil receiver error = %v, want ErrInvalidDocument", err)
	}
}

func TestDocumentRejectsInvalidUTF8Identity(t *testing.T) {
	for _, id := range []string{string([]byte{0xff}), string([]byte{0xfe})} {
		doc := &document.Document{ID: id, Text: "hello"}
		if err := doc.Validate(); !errors.Is(err, document.ErrInvalidDocument) {
			t.Fatalf("Validate: %v", err)
		}
		if _, err := json.Marshal(doc); !errors.Is(err, document.ErrInvalidDocument) {
			t.Fatalf("Marshal: %v", err)
		}
	}
	doc := &document.Document{ID: "文档", Text: "hello"}
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	var restored document.Document
	if err := json.Unmarshal(data, &restored); err != nil || restored.ID != doc.ID {
		t.Fatalf("roundtrip: %v %v", restored, err)
	}
}
