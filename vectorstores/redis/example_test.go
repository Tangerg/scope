package redis_test

import (
	"context"
	"os"
	"slices"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/vectorstores/redis"
)

type exampleBatcher struct{}

func (e exampleBatcher) Batch(_ context.Context, docs []*document.Document) ([][]*document.Document, error) {
	return slices.Collect(slices.Chunk(docs, 32)), nil
}

func ExampleNewStore() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	client := goredis.NewClient(&goredis.Options{Addr: os.Getenv("SCOPE_REDIS_ADDR"), Protocol: 3, Username: os.Getenv("SCOPE_REDIS_USERNAME"), Password: os.Getenv("SCOPE_REDIS_PASSWORD"), ContextTimeoutEnabled: true, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second})
	defer client.Close()
	// Provision once through the native host API, before constructing Store.
	if err := client.FTCreate(ctx, "documents", &goredis.FTCreateOptions{OnHash: true, Prefix: []any{"documents:"}}, &goredis.FieldSchema{FieldName: "embedding", FieldType: goredis.SearchFieldTypeVector, VectorArgs: &goredis.FTVectorArgs{FlatOptions: &goredis.FTFlatOptions{Type: "FLOAT32", Dim: 2, DistanceMetric: "COSINE"}}}).Err(); err != nil {
		panic(err)
	}
	model := embedding.ModelFunc(func(_ context.Context, request *embedding.Request) (*embedding.Response, error) {
		outputs := make([]*embedding.Output, len(request.Texts))
		for i := range outputs {
			outputs[i] = &embedding.Output{Embedding: []float64{1, 0}}
		}
		return embedding.NewResponse(outputs, nil)
	})
	store, err := redis.NewStore(ctx, redis.StoreConfig{Client: client, IndexName: "documents", EmbeddingModel: model, DocumentBatcher: exampleBatcher{}})
	if err != nil {
		panic(err)
	}
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
