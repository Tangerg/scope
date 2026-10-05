package pgstore_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
	"github.com/Tangerg/scope/vectorstores/postgres/internal/pgstore"
)

func TestSearchRejectsUnrepresentableFilterBeforeEmbedding(t *testing.T) {
	called := false
	store, err := pgstore.New(pgstore.Config{
		Provider: "test", MetadataColumn: "facts", DistanceMetric: pgstore.DistanceCosine,
		EmbeddingModel: embedding.ModelFunc(func(context.Context, *embedding.Request) (*embedding.Response, error) {
			called = true
			return nil, errors.New("unexpected embedding")
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	predicate, err := filter.Parse(`value[2147483648] == 1`)
	if err != nil {
		t.Fatal(err)
	}
	response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{Filter: predicate}})
	if err == nil || response != nil || called {
		t.Fatalf("Search = %#v, %v; embedding called = %t", response, err, called)
	}
}

func TestFilterOperationsPreserveClosedPoolErrors(t *testing.T) {
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
		t.Fatal("closed native pool acquired a connection")
	}
	for _, test := range []struct {
		source string
		calls  int
	}{
		{`value == 7`, 1},
		{`value > 7`, 0},
	} {
		t.Run(test.source, func(t *testing.T) {
			calls := 0
			store, err := pgstore.New(pgstore.Config{
				Provider: "test", Pool: pool, SchemaName: "public", TableName: "documents",
				MetadataColumn: "facts", DistanceMetric: pgstore.DistanceCosine,
				EmbeddingModel: embedding.ModelFunc(func(context.Context, *embedding.Request) (*embedding.Response, error) {
					calls++
					return embedding.NewResponse([]*embedding.Output{{Embedding: []float64{1, 0}}}, nil)
				}),
			})
			if err != nil {
				t.Fatal(err)
			}
			predicate, err := filter.Parse(test.source)
			if err != nil {
				t.Fatal(err)
			}
			response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{Filter: predicate}})
			if !errors.Is(err, closedErr) || response != nil || calls != test.calls {
				t.Fatalf("Search = %#v, %v; embedding calls = %d, want %d", response, err, calls, test.calls)
			}
			if err := store.DeleteWhere(t.Context(), predicate); !errors.Is(err, closedErr) || calls != test.calls {
				t.Fatalf("DeleteWhere = %v; embedding calls = %d", err, calls)
			}
		})
	}
}

func TestSearchPreservesEmbeddingFailureWithoutQuerying(t *testing.T) {
	failure := errors.New("embedding unavailable")
	store, err := pgstore.New(pgstore.Config{
		Provider: "test", MetadataColumn: "facts", DistanceMetric: pgstore.DistanceCosine,
		EmbeddingModel: embedding.ModelFunc(func(context.Context, *embedding.Request) (*embedding.Response, error) {
			return nil, failure
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query"})
	if !errors.Is(err, failure) || response != nil {
		t.Fatalf("Search = %#v, %v; want embedding failure", response, err)
	}
}
