package pgstore_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/vectorstores/postgres/internal/pgstore"
)

func TestConstructionPreservesClosedNativePoolError(t *testing.T) {
	config, err := pgxpool.ParseConfig("postgres://unused@localhost/unused?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.Host = t.TempDir()
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	pool.Close()
	_, closedErr := pool.Acquire(t.Context())
	if closedErr == nil {
		t.Fatal("closed pool acquired a connection")
	}
	called := false
	_, err = pgstore.New(t.Context(), pgstore.Config{Provider: "test", Pool: pool, SchemaName: "public", TableName: "documents", MetadataColumn: "facts", DistanceMetric: pgstore.DistanceCosine, DocumentBatcher: singleDocumentBatcher{}, EmbeddingModel: embedding.ModelFunc(func(context.Context, *embedding.Request) (*embedding.Response, error) {
		called = true
		return nil, errors.New("unexpected embedding")
	})})
	if !errors.Is(err, closedErr) || called {
		t.Fatalf("construct=%v embedding called=%t", err, called)
	}
}
