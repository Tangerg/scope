//go:build integration

package postgres_test

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Tangerg/scope/vectorstores/postgres"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
	"github.com/Tangerg/scope/core/vectorstore/storetest"
	"github.com/Tangerg/scope/vectorstores/postgres/cockroachdb"
	"github.com/Tangerg/scope/vectorstores/postgres/pgvector"
)

type nativeStore interface {
	vectorstore.Upserter
	vectorstore.Querier
	vectorstore.IDDeleter
	Scan(context.Context, vectorstore.ScanRequest) ([]*document.Document, error)
}

const fixtureSpace vectorstore.SpaceID = "native-test-space"

type nativeFixture struct {
	pool      *pgxpool.Pool
	store     nativeStore
	table     string
	vectorFor func(string) []float64
}

func newNativeFixture(t *testing.T, backend string, metric string) *nativeFixture {
	t.Helper()
	variable := "SCOPE_" + strings.ToUpper(backend) + "_DSN"
	dsn := os.Getenv(variable)
	if dsn == "" {
		t.Fatalf("%s is required with -tags=integration", variable)
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	schema := "scope_filter_" + strings.ToLower(rand.Text())
	f := &nativeFixture{pool: pool, table: schema + ".documents"}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 30*time.Second)
		defer cancel()
		if _, cleanupErr := pool.Exec(ctx, "DROP SCHEMA IF EXISTS "+schema+" CASCADE"); cleanupErr != nil {
			t.Errorf("cleanup isolated schema: %v", cleanupErr)
		}
	})
	switch backend {
	case "pgvector":
		f.store, err = pgvector.NewStore(t.Context(), pgvector.StoreConfig{
			Pool: pool, SchemaName: schema, TableName: "documents", MetadataColumn: "facts",
			Space: fixtureSpace, CreateDimensions: 2,
			IndexType: pgvector.IndexNone, DistanceMetric: postgres.DistanceMetric(metric),
		})
	case "cockroachdb":
		f.store, err = cockroachdb.NewStore(t.Context(), cockroachdb.StoreConfig{
			Pool: pool, SchemaName: schema, TableName: "documents", MetadataColumn: "facts",
			Space: fixtureSpace, CreateDimensions: 2, DistanceMetric: postgres.DistanceMetric(metric),
		})
	default:
		t.Fatalf("unknown test backend %q", backend)
	}
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (n *nativeFixture) install(ctx context.Context, docs []*document.Document) error {
	if _, err := n.pool.Exec(ctx, "DELETE FROM "+n.table); err != nil {
		return err
	}
	return n.upsert(ctx, docs)
}

func (n *nativeFixture) upsert(ctx context.Context, docs []*document.Document) error {
	records := make([]vectorstore.Record, len(docs))
	for i, doc := range docs {
		coords := []float64{1, 0}
		if n.vectorFor != nil {
			coords = n.vectorFor(doc.Text)
		}
		records[i] = vectorstore.Record{Document: doc, Values: coords}
	}
	return n.store.Upsert(ctx, &vectorstore.Batch{Space: fixtureSpace, Records: records})
}

func (n *nativeFixture) search(ctx context.Context, predicate filter.Predicate, limit int) ([]string, error) {
	res, err := n.store.Query(ctx, &vectorstore.Query{Space: fixtureSpace, Values: []float64{1, 0}, Options: vectorstore.SearchOptions{Filter: predicate, TopK: limit}})
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, r := range res.Results {
		ids = append(ids, r.Document.ID)
	}
	return ids, nil
}

func (n *nativeFixture) ids(ctx context.Context) ([]string, error) {
	rows, err := n.pool.Query(ctx, "SELECT id FROM "+n.table+" ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id []byte
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, string(id))
	}
	return ids, rows.Err()
}

// These are native capability tests: exact Core predicates on opaque bytes
// must be refused rather than widened or evaluated over a truncated result.
func TestLiveMetadataFilters(t *testing.T) {
	for _, backend := range []string{"pgvector", "cockroachdb"} {
		t.Run(backend, func(t *testing.T) {
			f := newNativeFixture(t, backend, "")
			t.Run("explicit exact scan", func(t *testing.T) {
				storetest.FilterConformance(t, storetest.FilterConfig{Query: func(ctx context.Context, docs []*document.Document, p filter.Predicate) ([]string, error) {
					if err := f.install(ctx, docs); err != nil {
						return nil, err
					}
					found, err := f.store.Scan(ctx, vectorstore.ScanRequest{Budget: vectorstore.ScanBudget{MaxRecords: len(docs), MaxBytes: 4 << 20, Timeout: time.Minute}, Filter: p})
					if err != nil {
						return nil, err
					}
					var ids []string
					for _, doc := range found {
						ids = append(ids, doc.ID)
					}
					return ids, nil
				}})
			})
			docs := nativeDocuments(t, []any{7, "7", uint64(18446744073709551615), nil, []any{"x"}})
			if err := f.install(t.Context(), docs); err != nil {
				t.Fatal(err)
			}
			for _, budget := range []vectorstore.ScanBudget{{MaxRecords: len(docs) - 1, MaxBytes: 4 << 20, Timeout: time.Minute}, {MaxRecords: len(docs), MaxBytes: 1, Timeout: time.Minute}} {
				if partial, err := f.store.Scan(t.Context(), vectorstore.ScanRequest{Budget: budget}); !errors.Is(err, vectorstore.ErrScanBudget) || partial != nil {
					t.Fatalf("incomplete scan: docs=%v err=%v", partial, err)
				}
			}
			for _, source := range []string{
				"value == 7", "value == 18446744073709551615", "value[0] == 'x'", "value[2147483648] is null", "not (value has 'x')", "value like 'x%'",
			} {
				if ids, err := f.search(t.Context(), nativePredicate(t, source), 1); !errors.Is(err, vectorstore.ErrUnsupportedFilter) || len(ids) != 0 {
					t.Fatalf("predicate %q: ids=%v err=%v", source, ids, err)
				}
			}
			if _, claimed := f.store.(vectorstore.FilterDeleter); claimed {
				t.Fatal("exposed conditional deletion on opaque metadata")
			}
			ids, err := f.ids(t.Context())
			if err != nil || len(ids) != len(docs) {
				t.Fatalf("IDs after rejected predicates: %v, %v", ids, err)
			}
		})
	}
}

func nativeDocuments(t *testing.T, values []any) []*document.Document {
	t.Helper()
	docs := make([]*document.Document, len(values))
	for index, value := range values {
		facts, err := metadata.FromValues(map[string]any{"value": value})
		if err != nil {
			t.Fatal(err)
		}
		docs[index] = &document.Document{ID: fmt.Sprintf("doc-%02d", index), Text: "filter conformance", Metadata: facts}
	}
	return docs
}

func nativePredicate(t *testing.T, source string) filter.Predicate {
	t.Helper()
	predicate, err := filter.Parse(source)
	if err != nil {
		t.Fatal(err)
	}
	return predicate
}
