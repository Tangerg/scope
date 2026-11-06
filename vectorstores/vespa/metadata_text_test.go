package vespa

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

func TestMetadataEscapesNativeNoncharactersWithoutChangingFacts(t *testing.T) {
	var characters strings.Builder
	for character := rune(0xfdd0); character <= 0xfddf; character++ {
		characters.WriteRune(character)
	}
	for plane := range 17 {
		characters.WriteRune(rune(plane<<16 | 0xfffe))
		characters.WriteRune(rune(plane<<16 | 0xffff))
	}
	value := characters.String()
	key := "key" + value
	facts, err := metadata.FromValues(map[string]any{key: value, "selection": value, "literal": `literal \ufdd0`, "nested": map[string]any{"value": []string{value, `literal \ufdd0`}}})
	if err != nil {
		t.Fatal(err)
	}
	facts["exact"] = json.RawMessage(`1e1000`)
	doc := &document.Document{ID: "unicode-metadata", Text: "text", Metadata: facts,
		Provenance: &document.Provenance{Chunks: []document.Chunk{{Parent: value, Index: 0, Total: 1}}}}
	if validationErr := doc.Validate(); validationErr != nil {
		t.Fatal(validationErr)
	}
	store, fixture := newFixtureStore(t, StoreConfig{})
	if writeErr := store.Upsert(t.Context(), fixtureBatch([]*document.Document{{ID: "first", Text: "text"}, doc})); writeErr != nil {
		t.Fatal(writeErr)
	}
	encoded, present, err := fixture.rows[doc.ID].Decode[string](metadataField)
	if err != nil || !present {
		t.Fatalf("native metadata=%q present=%t error=%v", encoded, present, err)
	}
	for _, character := range value {
		if strings.ContainsRune(encoded, character) {
			t.Fatalf("native string contains forbidden code point U+%04X", character)
		}
	}
	for _, escape := range []string{`\ufdd0`, `\ufddf`, `\ufffe`, `\uffff`, `\ud83f\udffe`, `\udbff\udfff`} {
		if !strings.Contains(encoded, escape) {
			t.Fatalf("native metadata lacks lossless JSON escape %s", escape)
		}
	}
	predicate := filter.EQ("selection", value)
	response, err := store.QueryByScan(t.Context(), &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("text"), Options: vectorstore.SearchOptions{Filter: predicate}}, fixtureBudget)
	if err != nil || len(response.Results) != 1 {
		t.Fatalf("query=%v error=%v", response, err)
	}
	restored := response.Results[0].Document
	wantValues, err := doc.Metadata.Values()
	if err != nil {
		t.Fatal(err)
	}
	gotValues, err := restored.Metadata.Values()
	if err != nil {
		t.Fatal(err)
	}
	if restored.ID != doc.ID || !reflect.DeepEqual(gotValues, wantValues) || !bytes.Equal(restored.Metadata["exact"], facts["exact"]) || !bytes.Equal(restored.Metadata["literal"], facts["literal"]) || !reflect.DeepEqual(restored.Provenance, doc.Provenance) {
		t.Fatalf("restored facts differ: got=%+v want=%+v", restored, doc)
	}
	if err := store.DeleteMatching(t.Context(), predicate, fixtureBudget); err != nil {
		t.Fatal(err)
	}
	if _, exists := fixture.rows[doc.ID]; exists || len(fixture.rows) != 1 {
		t.Fatalf("conditional deletion lost escaped metadata identity: %v", fixture.rows)
	}
}

func TestInnerJSONEscapingKeepsOrdinaryTextAndExistingEscapes(t *testing.T) {
	for _, raw := range []string{`null`, `{}`, `{"line":"a\nb","literal":"\\ufdd0","value":"中文😀","number":9007199254740993}`} {
		if encoded := escapeMetadataJSON([]byte(raw)); encoded != raw {
			t.Fatalf("ordinary JSON changed: got=%s want=%s", encoded, raw)
		}
	}
}
