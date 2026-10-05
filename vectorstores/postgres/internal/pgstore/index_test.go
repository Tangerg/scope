package pgstore_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/vectorstores/postgres/internal/pgstore"
)

type singleDocumentBatcher struct{}

func (s singleDocumentBatcher) Batch(_ context.Context, documents []*document.Document) ([][]*document.Document, error) {
	batches := make([][]*document.Document, len(documents))
	for index, doc := range documents {
		batches[index] = []*document.Document{doc}
	}
	return batches, nil
}

func TestIndexRejectsMediaInLaterBatchBeforeEmbedding(t *testing.T) {
	content, err := media.NewBytes("image/png", []byte("image"))
	if err != nil {
		t.Fatal(err)
	}
	request, err := vectorstore.NewIndexRequest([]*document.Document{
		{ID: "text", Text: "text-only document"},
		{ID: "mixed", Text: "caption", Media: content},
	})
	if err != nil {
		t.Fatal(err)
	}
	store := new(pgstore.Store)
	if indexErr := store.Index(t.Context(), request); !errors.Is(indexErr, vectorstore.ErrInvalidDocument) {
		t.Fatalf("Index = %v", indexErr)
	}
}
