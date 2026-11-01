package chroma_test

import (
	"context"
	"time"

	v2 "github.com/amikos-tech/chroma-go/pkg/api/v2"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/vectorstores/chroma"
)

func ExampleNewStore() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, err := v2.NewHTTPClient(v2.WithBaseURL("http://localhost:8000"))
	if err != nil {
		panic(err)
	}
	defer client.Close()
	// The host already set scope.vector_space and fixed the native dimension.
	collection, err := client.GetCollection(ctx, "documents")
	if err != nil {
		panic(err)
	}
	defer collection.Close()
	store, err := chroma.NewStore(ctx, chroma.StoreConfig{Collection: collection, Space: "example-v1"})
	if err != nil {
		panic(err)
	}
	if err = store.Upsert(ctx, &vectorstore.Batch{Space: "example-v1", Records: []vectorstore.Record{{Document: &document.Document{ID: "1", Text: "example"}, Values: []float64{1, 0}}}}); err != nil {
		panic(err)
	}
	if _, err = store.Query(ctx, &vectorstore.Query{Space: "example-v1", Values: []float64{1, 0}}); err != nil {
		panic(err)
	}
}
