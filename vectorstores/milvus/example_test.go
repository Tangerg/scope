package milvus_test

import (
	"context"
	"os"

	"github.com/milvus-io/milvus/client/v3/milvusclient"

	"github.com/Tangerg/scope/core/document"

	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/vectorstores/milvus"
)

func ExampleNewStore() {
	ctx := context.Background()
	client, err := milvusclient.New(ctx, &milvusclient.ClientConfig{Address: os.Getenv("SCOPE_MILVUS_ADDRESS")})
	if err != nil {
		panic(err)
	}
	defer client.Close(ctx)
	const space vectorstore.SpaceID = "encoder-v1"
	store, err := milvus.NewStore(ctx, milvus.StoreConfig{Client: client, CollectionName: "documents", Space: space})
	if err != nil {
		panic(err)
	}
	batch := &vectorstore.Batch{Space: space, Records: []vectorstore.Record{{Document: &document.Document{ID: "one", Text: "example"}, Values: []float64{1, 0}}}}
	if err = store.Upsert(ctx, batch); err != nil {
		panic(err)
	}
	if _, err = store.Query(ctx, &vectorstore.Query{Space: space, Values: []float64{1, 0}}); err != nil {
		panic(err)
	}
	if err := store.DeleteIDs(ctx, []string{"one"}); err != nil {
		panic(err)
	}
}
