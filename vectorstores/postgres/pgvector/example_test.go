package pgvector_test

import (
	"context"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/vectorstores/postgres/pgvector"
)

type documentBatcher struct{}

func (d documentBatcher) Batch(_ context.Context, documents []*document.Document) ([][]*document.Document, error) {
	return [][]*document.Document{documents}, nil
}

func ExampleNewStore() {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, os.Getenv("SCOPE_PGVECTOR_DSN"))
	if err != nil {
		panic(err)
	}
	defer pool.Close()
	model := embedding.ModelFunc(func(_ context.Context, request *embedding.Request) (*embedding.Response, error) {
		outputs := make([]*embedding.Output, len(request.Texts))
		for index := range outputs {
			outputs[index] = &embedding.Output{Embedding: []float64{1, 0}}
		}
		return embedding.NewResponse(outputs, nil)
	})
	store, err := pgvector.NewStore(ctx, pgvector.StoreConfig{Pool: pool, SchemaName: "scope", TableName: "documents", EmbeddingModel: model, DocumentBatcher: documentBatcher{}, Dimensions: 2, InitializeSchema: true})
	if err != nil {
		panic(err)
	}
	if err = store.Index(ctx, &vectorstore.IndexRequest{Documents: []*document.Document{{ID: "one", Text: "current record"}}}); err != nil {
		panic(err)
	}
	if _, err = store.Search(ctx, &vectorstore.SearchRequest{Query: "current"}); err != nil {
		panic(err)
	}
	if err = store.DeleteIDs(ctx, []string{"one"}); err != nil {
		panic(err)
	}
}
