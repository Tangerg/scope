package qdrant_test

import (
	"context"
	"os"
	"slices"
	"time"

	qdrantclient "github.com/qdrant/go-client/qdrant"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/vectorstores/qdrant"
)

type exampleBatcher struct{}

func (e exampleBatcher) Batch(_ context.Context, docs []*document.Document) ([][]*document.Document, error) {
	return slices.Collect(slices.Chunk(docs, 32)), nil
}

func ExampleNewStore() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	client, err := qdrantclient.NewClient(&qdrantclient.Config{Host: os.Getenv("QDRANT_HOST"), APIKey: os.Getenv("QDRANT_API_KEY")})
	if err != nil {
		panic(err)
	}
	defer func() {
		if closeErr := client.Close(); closeErr != nil {
			panic(closeErr)
		}
	}()
	// The host provisions one unnamed FLOAT32 vector through the native API.
	if err = client.CreateCollection(ctx, &qdrantclient.CreateCollection{CollectionName: "documents", VectorsConfig: qdrantclient.NewVectorsConfig(&qdrantclient.VectorParams{Size: 2, Distance: qdrantclient.Distance_Cosine})}); err != nil {
		panic(err)
	}
	model := embedding.ModelFunc(func(_ context.Context, request *embedding.Request) (*embedding.Response, error) {
		outputs := make([]*embedding.Output, len(request.Texts))
		for i := range outputs {
			outputs[i] = &embedding.Output{Embedding: []float64{1, 0}}
		}
		return embedding.NewResponse(outputs, nil)
	})
	store, err := qdrant.NewStore(ctx, qdrant.StoreConfig{Client: client, CollectionName: "documents", EmbeddingModel: model, DocumentBatcher: exampleBatcher{}})
	if err != nil {
		panic(err)
	}
	if err = store.Index(ctx, &vectorstore.IndexRequest{Documents: []*document.Document{{ID: "1", Text: "example"}}}); err != nil {
		panic(err)
	}
	if _, err = store.Search(ctx, &vectorstore.SearchRequest{Query: "example"}); err != nil {
		panic(err)
	}
	if err = store.DeleteIDs(ctx, []string{"1"}); err != nil {
		panic(err)
	}
}
