package pgstore_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
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
	called := false
	store, err := pgstore.New(pgstore.Config{
		Provider: "test", DistanceMetric: pgstore.DistanceCosine,
		DocumentBatcher: singleDocumentBatcher{},
		EmbeddingModel: embedding.ModelFunc(func(context.Context, *embedding.Request) (*embedding.Response, error) {
			called = true
			return nil, errors.New("unexpected embedding")
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if indexErr := store.Index(t.Context(), request); !errors.Is(indexErr, vectorstore.ErrInvalidDocument) || called {
		t.Fatalf("Index = %v, embedding called = %t", indexErr, called)
	}
}
