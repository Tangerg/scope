package weaviate_test

import (
	"context"
	"time"

	weaviateclient "github.com/weaviate/weaviate-go-client/v5/weaviate"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/vectorstores/weaviate"
)

type exampleBatcher struct{}

func (e exampleBatcher) Batch(_ context.Context, docs []*document.Document) ([][]*document.Document, error) {
	return [][]*document.Document{docs}, nil
}

func ExampleNewStore() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	client, err := weaviateclient.NewClient(weaviateclient.Config{Host: "localhost:8080", Scheme: "http"})
	if err != nil {
		panic(err)
	}
	model := embedding.ModelFunc(func(_ context.Context, request *embedding.Request) (*embedding.Response, error) {
		outputs := make([]*embedding.Output, len(request.Texts))
		for i := range outputs {
			outputs[i] = &embedding.Output{Embedding: []float64{1, 0}}
		}
		return embedding.NewResponse(outputs, nil)
	})
	// The host has provisioned the current native collection described in GoDoc.
	store, err := weaviate.NewStore(ctx, weaviate.StoreConfig{Client: client, ClassName: "Documents", EmbeddingModel: model, DocumentBatcher: exampleBatcher{}})
	if err != nil {
		panic(err)
	}
	id := "f9168c5e-ceb2-4faa-b6bf-329bf39fa1e4"
	if err = store.Index(ctx, &vectorstore.IndexRequest{Documents: []*document.Document{{ID: id, Text: "hello"}}}); err != nil {
		panic(err)
	}
	if _, err = store.Search(ctx, &vectorstore.SearchRequest{Query: "hello"}); err != nil {
		panic(err)
	}
	if err = store.DeleteIDs(ctx, []string{id}); err != nil {
		panic(err)
	}
}
