package pgstore

import (
	"encoding/json"
	"testing"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

func TestCoreBytesOwnMetadataAndIdentity(t *testing.T) {
	for name, facts := range map[string]metadata.Map{"null": nil, "empty": {}, "exact": {"value": json.RawMessage(`{"exact":9007199254740993,"scale":1e3,"huge":1e1000000,"nul":"\u0000"}`)}} {
		t.Run(name, func(t *testing.T) {
			encoded, err := facts.MarshalJSON()
			if err != nil {
				t.Fatal(err)
			}
			doc, err := decodeDocument([]byte("native\x00文"), "text", encoded)
			if err != nil || doc.ID != "native\x00文" || (doc.Metadata == nil) != (facts == nil) || !doc.Metadata.Equal(facts) {
				t.Fatalf("native bytes: document=%v error=%v", doc, err)
			}
		})
	}
	for _, facts := range [][]byte{nil, {}, []byte("[]"), []byte("broken")} {
		if _, err := decodeDocument([]byte("one"), "text", facts); err == nil {
			t.Fatalf("malformed Core bytes %q accepted", facts)
		}
	}
	if _, err := decodeDocument(nil, "text", []byte("null")); err == nil {
		t.Fatal("missing native ID accepted")
	}
	if _, err := decodeDocument([]byte("one"), "", []byte("null")); err == nil {
		t.Fatal("missing content accepted")
	}
}

func TestCoreSelectsAllRowsBeforeEffects(t *testing.T) {
	facts, err := metadata.FromValues(map[string]any{"value": 1})
	if err != nil {
		t.Fatal(err)
	}
	predicate, err := filter.Parse("value[2147483648] is null")
	if err != nil {
		t.Fatal(err)
	}
	docs := []*document.Document{{ID: "one", Text: "text", Metadata: facts}}
	ids, err := selectIDs(docs, predicate)
	if err != nil || len(ids) != 1 || string(ids[0]) != "one" {
		t.Fatalf("native int4 restriction leaked into Core: ids=%v error=%v", ids, err)
	}
	if err = docs[0].Metadata.Set("value", "wrong type"); err != nil {
		t.Fatal(err)
	}
	predicate, err = filter.Parse("value < 2")
	if err != nil {
		t.Fatal(err)
	}
	if ids, err := selectIDs(docs, predicate); err == nil || ids != nil {
		t.Fatalf("type error concealed: ids=%v error=%v", ids, err)
	}
}
