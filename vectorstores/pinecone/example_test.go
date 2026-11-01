package pinecone_test

import (
	"context"
	"os"

	"time"

	pineconesdk "github.com/pinecone-io/go-pinecone/v7/pinecone"

	"github.com/Tangerg/scope/core/document"

	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/vectorstores/pinecone"
)

func ExampleNewStore() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	client, err := pineconesdk.NewClient(pineconesdk.NewClientParams{ApiKey: os.Getenv("PINECONE_API_KEY")})
	if err != nil {
		panic(err)
	}
	// The host provisions a ready 2-dimensional dense serverless index once.
	store, err := pinecone.NewStore(ctx, pinecone.StoreConfig{Client: client, IndexName: os.Getenv("PINECONE_INDEX_NAME"), Namespace: "documents", Space: "example-v1"})
	if err != nil {
		panic(err)
	}
	defer func() {
		if closeErr := store.Close(); closeErr != nil {
			panic(closeErr)
		}
	}()
	if err = store.Upsert(ctx, &vectorstore.Batch{Space: "example-v1", Records: []vectorstore.Record{{Document: &document.Document{ID: "one", Text: "example"}, Values: []float64{1, 0}}}}); err != nil {
		panic(err)
	}
	if _, err = store.Query(ctx, &vectorstore.Query{Space: "example-v1", Values: []float64{1, 0}}); err != nil {
		panic(err)
	}
	if err = store.DeleteIDs(ctx, []string{"one"}); err != nil {
		panic(err)
	}
}
