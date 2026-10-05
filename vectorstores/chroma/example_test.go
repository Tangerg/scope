package chroma_test

import (
	"context"
	"slices"
	"time"

	v2 "github.com/amikos-tech/chroma-go/pkg/api/v2"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/vectorstores/chroma"
)

type exampleBatcher struct{}

func (e exampleBatcher) Batch(_ context.Context, docs []*document.Document) ([][]*document.Document, error) {
	return slices.Collect(slices.Chunk(docs, 32)), nil
}

func ExampleNewStore() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, err := v2.NewHTTPClient(v2.WithBaseURL("http://localhost:8000"))
	if err != nil {
		panic(err)
	}
	defer client.Close()
	collection, err := client.GetCollection(ctx, "documents")
	if err != nil {
		panic(err)
	}
	defer collection.Close()
	model := embedding.ModelFunc(func(_ context.Context, request *embedding.Request) (*embedding.Response, error) {
		outputs := make([]*embedding.Output, len(request.Texts))
		for i := range outputs {
			outputs[i] = &embedding.Output{Embedding: []float64{1, 0}}
		}
		return embedding.NewResponse(outputs, nil)
	})
	store, err := chroma.NewStore(ctx, chroma.StoreConfig{
		Collection: collection, EmbeddingModel: model, DocumentBatcher: exampleBatcher{},
	})
	if err != nil {
		panic(err)
	}
	if _, err = store.Search(ctx, &vectorstore.SearchRequest{Query: "example"}); err != nil {
		panic(err)
	}
}
