package postgres_test

import (
	"errors"
	"testing"

	"github.com/Tangerg/scope/vectorstores/postgres"

	"github.com/jackc/pgx/v5/pgxpool"
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
	_, err = postgres.NewStore(t.Context(), postgres.StoreConfig{Pool: pool, SchemaName: "public", TableName: "documents", MetadataColumn: "facts", DistanceMetric: postgres.DistanceCosine, Space: "test"})
	if !errors.Is(err, closedErr) {
		t.Fatalf("construct = %v", err)
	}
}
