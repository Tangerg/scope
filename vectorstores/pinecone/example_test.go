package pinecone_test

import (
	"context"
	"os"
	"slices"
	"time"

	pineconesdk "github.com/pinecone-io/go-pinecone/v4/pinecone"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/vectorstores/pinecone"
)

type exampleBatcher struct{}

func (e exampleBatcher) Batch(_ context.Context, docs []*document.Document) ([][]*document.Document, error) {
	return slices.Collect(slices.Chunk(docs, 32)), nil
}

func ExampleNewStore() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	client, err := pineconesdk.NewClient(pineconesdk.NewClientParams{ApiKey: os.Getenv("PINECONE_API_KEY")})
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
	// The host provisions a ready 2-dimensional dense serverless index once.
	store, err := pinecone.NewStore(ctx, pinecone.StoreConfig{Client: client, IndexName: os.Getenv("PINECONE_INDEX_NAME"), Namespace: "documents", EmbeddingModel: model, DocumentBatcher: exampleBatcher{}})
	if err != nil {
		panic(err)
	}
	defer func() {
		if closeErr := store.Close(); closeErr != nil {
			panic(closeErr)
		}
	}()
	if err = store.Index(ctx, &vectorstore.IndexRequest{Documents: []*document.Document{{ID: "one", Text: "example"}}}); err != nil {
		panic(err)
	}
	if _, err = store.Search(ctx, &vectorstore.SearchRequest{Query: "example"}); err != nil {
		panic(err)
	}
	if err = store.DeleteIDs(ctx, []string{"one"}); err != nil {
		panic(err)
	}
}
