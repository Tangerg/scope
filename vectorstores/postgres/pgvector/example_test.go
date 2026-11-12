package pgvector_test

import (
	"context"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/vectorstores/postgres/pgvector"
)

func ExampleNewStore() {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, os.Getenv("SCOPE_PGVECTOR_DSN"))
	if err != nil {
		panic(err)
	}
	defer pool.Close()
	store, err := pgvector.NewStore(ctx, pgvector.StoreConfig{Pool: pool, SchemaName: "scope", TableName: "documents", Space: "release-notes-v1", CreateDimensions: 2})
	if err != nil {
		panic(err)
	}
	if err = store.Upsert(ctx, &vectorstore.Batch{Space: "release-notes-v1", Records: []vectorstore.Record{{Document: &document.Document{ID: "one", Text: "current record"}, Values: []float64{1, 0}}}}); err != nil {
		panic(err)
	}
	if _, err = store.Query(ctx, &vectorstore.Query{Space: "release-notes-v1", Values: []float64{1, 0}}); err != nil {
		panic(err)
	}
	if err = store.DeleteIDs(ctx, []string{"one"}); err != nil {
		panic(err)
	}
}
