package redis_test

import (
	"context"
	"os"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/vectorstores/redis"
)

func ExampleNewStore() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	client := goredis.NewClient(&goredis.Options{Addr: os.Getenv("SCOPE_REDIS_ADDR"), Protocol: 3, Username: os.Getenv("SCOPE_REDIS_USERNAME"), Password: os.Getenv("SCOPE_REDIS_PASSWORD"), ContextTimeoutEnabled: true, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second})
	defer client.Close()
	const space vectorstore.SpaceID = "paired-v1"
	// Provision once through the native host API, before constructing Store.
	if err := client.FTCreate(ctx, "documents", &goredis.FTCreateOptions{OnHash: true, Prefix: []any{"documents:" + space.StorageKey() + ":"}}, &goredis.FieldSchema{FieldName: "embedding", FieldType: goredis.SearchFieldTypeVector, VectorArgs: &goredis.FTVectorArgs{FlatOptions: &goredis.FTFlatOptions{Type: "FLOAT32", Dim: 2, DistanceMetric: "COSINE"}}}).Err(); err != nil {
		panic(err)
	}
	store, err := redis.NewStore(ctx, redis.StoreConfig{Client: client, ScanNodes: func(ctx context.Context, visit func(context.Context, *goredis.Client) error) error {
		return visit(ctx, client)
	}, IndexName: "documents", Space: space})
	if err != nil {
		panic(err)
	}
	if err = store.Upsert(ctx, &vectorstore.Batch{Space: space, Records: []vectorstore.Record{{Document: &document.Document{ID: "one", Text: "example"}, Values: []float64{1, 0}}}}); err != nil {
		panic(err)
	}
	if _, err = store.Query(ctx, &vectorstore.Query{Space: space, Values: []float64{1, 0}}); err != nil {
		panic(err)
	}
	if err = store.DeleteIDs(ctx, []string{"one"}); err != nil {
		panic(err)
	}
}
