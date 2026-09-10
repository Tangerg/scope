package inmemory_test

import (
	"reflect"
	"testing"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/core/vectorstore"
)

func TestStorePreservesMixedDocumentContent(t *testing.T) {
	store := newStore(t)
	content, err := media.NewURI("image/png", "https://example.com/image.png")
	if err != nil {
		t.Fatal(err)
	}
	doc := &document.Document{ID: "mixed", Text: "image caption", Media: content}
	want := doc.Clone()
	if indexErr := store.Index(t.Context(), &vectorstore.IndexRequest{Documents: []*document.Document{doc}}); indexErr != nil {
		t.Fatal(indexErr)
	}
	doc.Media = nil
	response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "image caption"})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 1 || !reflect.DeepEqual(response.Results[0].Document, want) || response.Results[0].Document.Media == content {
		t.Fatalf("retrieved documents = %#v, want independently owned text and media", response.Results)
	}
}
