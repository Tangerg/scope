package mongodb_test

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/Tangerg/scope/core/document"

	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/vectorstores/mongodb"
)

func ExampleNewStore() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	client, err := mongo.Connect(options.Client().ApplyURI("mongodb://localhost:27017/?directConnection=true"))
	if err != nil {
		panic(err)
	}
	defer func() {
		if closeErr := client.Disconnect(context.WithoutCancel(ctx)); closeErr != nil {
			panic(closeErr)
		}
	}()
	// The host has provisioned this collection and a READY native vector index.
	store, err := mongodb.NewStore(ctx, mongodb.StoreConfig{Collection: client.Database("scope").Collection("documents"), VectorIndexName: "vector_index", Space: "example-v1"})
	if err != nil {
		panic(err)
	}
	if err = store.Upsert(ctx, &vectorstore.Batch{Space: "example-v1", Records: []vectorstore.Record{{Document: &document.Document{ID: "one", Text: "hello"}, Values: []float64{1, 0}}}}); err != nil {
		panic(err)
	}
	if _, err = store.Query(ctx, &vectorstore.Query{Space: "example-v1", Values: []float64{1, 0}}); err != nil {
		panic(err)
	}
	if err = store.DeleteIDs(ctx, []string{"one"}); err != nil {
		panic(err)
	}
}
