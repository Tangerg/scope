package weaviate_test

import (
	"context"
	"time"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/vectorstores/weaviate"
)

func ExampleNewStore() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	client, err := weaviate.NewClient(ctx, weaviate.ClientConfig{Endpoint: "http://localhost:8080", GRPCAddress: "localhost:50051"})
	if err != nil {
		panic(err)
	}
	// The host has provisioned the current native collection described in GoDoc.
	defer client.Close()
	store, err := weaviate.NewStore(ctx, weaviate.StoreConfig{Client: client, Collection: "Documents", Space: "paired-v1"})
	if err != nil {
		panic(err)
	}
	id := "f9168c5e-ceb2-4faa-b6bf-329bf39fa1e4"
	if err = store.Upsert(ctx, &vectorstore.Batch{Space: "paired-v1", Records: []vectorstore.Record{{Document: &document.Document{ID: id, Text: "hello"}, Values: []float64{1, 0}}}}); err != nil {
		panic(err)
	}
	if _, err = store.Query(ctx, &vectorstore.Query{Space: "paired-v1", Values: []float64{1, 0}}); err != nil {
		panic(err)
	}
	if err = store.DeleteIDs(ctx, []string{id}); err != nil {
		panic(err)
	}
}
