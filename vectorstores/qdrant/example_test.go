package qdrant_test

import (
	"context"
	"os"
	"time"

	qdrantclient "github.com/qdrant/go-client/qdrant"

	"github.com/Tangerg/scope/core/document"

	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/vectorstores/qdrant"
)

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
	if err = client.CreateCollection(ctx, &qdrantclient.CreateCollection{CollectionName: "documents", Metadata: map[string]*qdrantclient.Value{qdrant.SpaceMetadataKey: qdrantclient.NewValueString("example-v1")}, VectorsConfig: qdrantclient.NewVectorsConfig(&qdrantclient.VectorParams{Size: 2, Distance: qdrantclient.Distance_Cosine})}); err != nil {
		panic(err)
	}
	store, err := qdrant.NewStore(ctx, qdrant.StoreConfig{Client: client, CollectionName: "documents", Space: "example-v1"})
	if err != nil {
		panic(err)
	}
	if err = store.Upsert(ctx, &vectorstore.Batch{Space: "example-v1", Records: []vectorstore.Record{{Document: &document.Document{ID: "1", Text: "example"}, Values: []float64{1, 0}}}}); err != nil {
		panic(err)
	}
	if _, err = store.Query(ctx, &vectorstore.Query{Space: "example-v1", Values: []float64{1, 0}}); err != nil {
		panic(err)
	}
	if err = store.DeleteIDs(ctx, []string{"1"}); err != nil {
		panic(err)
	}
}
