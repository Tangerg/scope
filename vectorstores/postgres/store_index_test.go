package postgres_test

import (
	"errors"
	"testing"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/vectorstores/postgres"
)

func TestIndexRejectsMediaInLaterBatchBeforeEmbedding(t *testing.T) {
	content, err := media.NewBytes("image/png", []byte("image"))
	if err != nil {
		t.Fatal(err)
	}
	docs := []*document.Document{
		{ID: "text", Text: "text-only document"},
		{ID: "mixed", Text: "caption", Media: content},
	}
	store := new(postgres.Store)
	if indexErr := store.CheckDocuments(t.Context(), "test", docs); !errors.Is(indexErr, vectorstore.ErrInvalidDocument) {
		t.Fatalf("Index = %v", indexErr)
	}
}
