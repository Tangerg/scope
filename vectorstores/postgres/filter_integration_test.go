//go:build integration

package postgres_test

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
	"github.com/Tangerg/scope/core/vectorstore/storetest"
	"github.com/Tangerg/scope/vectorstores/postgres/cockroachdb"
	"github.com/Tangerg/scope/vectorstores/postgres/pgvector"
)

type nativeStore interface {
	vectorstore.Indexer
	vectorstore.Searcher
	vectorstore.FilterDeleter
	vectorstore.IDDeleter
}

type nativeFixture struct {
	pool            *pgxpool.Pool
	store           nativeStore
	table           string
	embeddingCalls  atomic.Int64
	beforeEmbedding func(context.Context) error
	vectorFor       func(string) []float64
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
	model := embedding.ModelFunc(func(ctx context.Context, request *embedding.Request) (*embedding.Response, error) {
		f.embeddingCalls.Add(1)
		if f.beforeEmbedding != nil {
			if embeddingErr := f.beforeEmbedding(ctx); embeddingErr != nil {
				return nil, embeddingErr
			}
		}
		outputs := make([]*embedding.Output, len(request.Texts))
		for index, text := range request.Texts {
			vector := []float64{1, 0}
			if f.vectorFor != nil {
				vector = f.vectorFor(text)
			}
			outputs[index] = &embedding.Output{Embedding: vector}
		}
		return embedding.NewResponse(outputs, nil)
	})
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
			EmbeddingModel: model, DocumentBatcher: nativeBatcher{}, Dimensions: 2,
			IndexType: pgvector.IndexNone, InitializeSchema: true, DistanceMetric: pgvector.DistanceMetric(metric),
		})
	case "cockroachdb":
		f.store, err = cockroachdb.NewStore(t.Context(), cockroachdb.StoreConfig{
			Pool: pool, SchemaName: schema, TableName: "documents", MetadataColumn: "facts",
			EmbeddingModel: model, DocumentBatcher: nativeBatcher{}, Dimensions: 2, InitializeSchema: true, DistanceMetric: cockroachdb.DistanceMetric(metric),
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
	return n.store.Index(ctx, &vectorstore.IndexRequest{Documents: docs})
}

func (n *nativeFixture) search(ctx context.Context, predicate filter.Predicate, limit int) ([]string, error) {
	response, err := n.store.Search(ctx, &vectorstore.SearchRequest{
		Query: "filter conformance", Options: vectorstore.SearchOptions{TopK: limit, Filter: predicate},
	})
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, hit := range response.Results {
		ids = append(ids, hit.Document.ID)
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

func (n *nativeFixture) delete(ctx context.Context, docs []*document.Document, predicate filter.Predicate) ([]string, error) {
	if err := n.store.DeleteWhere(ctx, predicate); err != nil {
		return nil, err
	}
	remaining, err := n.ids(ctx)
	if err != nil {
		return nil, err
	}
	var deleted []string
	for _, doc := range docs {
		if !slices.Contains(remaining, doc.ID) {
			deleted = append(deleted, doc.ID)
		}
	}
	return deleted, nil
}

type nativeBatcher struct{}

func (n nativeBatcher) Batch(_ context.Context, documents []*document.Document) ([][]*document.Document, error) {
	return [][]*document.Document{documents}, nil
}

// Each explicitly selected backend creates and removes its own isolated schema.
func TestLiveMetadataFilters(t *testing.T) {
	for _, backend := range []string{"pgvector", "cockroachdb"} {
		t.Run(backend, func(t *testing.T) {
			fixture := newNativeFixture(t, backend, "")
			for _, operation := range []string{"search", "delete"} {
				t.Run(operation, func(t *testing.T) {
					storetest.FilterConformance(t, storetest.FilterConfig{Query: func(ctx context.Context, docs []*document.Document, predicate filter.Predicate) ([]string, error) {
						if err := fixture.install(ctx, docs); err != nil {
							return nil, err
						}
						if operation == "delete" {
							return fixture.delete(ctx, docs, predicate)
						}
						return fixture.search(ctx, predicate, len(docs))
					}})
				})
			}
			t.Run("scalar_types", func(t *testing.T) {
				for _, test := range []struct {
					source string
					values []any
					want   []int
				}{
					{`value == 7`, []any{7, "7", true, []any{7}, map[string]any{}, nil}, []int{0}},
					{`value == true`, []any{true, "true", 1, []any{true}, map[string]any{}, nil}, []int{0}},
					{`value == '{}'`, []any{"{}", map[string]any{}, []any{}}, []int{0}},
					{`value in (7, 18446744073709551615)`, []any{7, "7", uint64(18446744073709551615), "18446744073709551615"}, []int{0, 2}},
					{`value in (-9223372036854775808, 18446744073709551615, 0.1)`, []any{int64(-9223372036854775808), uint64(18446744073709551615), 0.1, "0.1"}, []int{0, 1, 2}},
					{`value == 18446744073709551615`, []any{uint64(18446744073709551615), "18446744073709551615"}, []int{0}},
					{`value has 18446744073709551615`, []any{[]any{uint64(18446744073709551615)}, []any{"18446744073709551615"}, nil}, []int{0}},
					{`not (value has 'x')`, []any{nil, "x", []any{"x"}, []any{"y"}}, []int{0, 1, 3}},
					{`value like 'a\\%b'`, []any{`a\pathb`, "apathb", "ab"}, []int{0}},
					{`value[0][0] == 1`, []any{[]any{[]any{1}}, []any{1}, 1, map[string]any{"0": []any{1}}}, []int{0}},
					{`not (value[0][0] == 1)`, []any{[]any{[]any{1}}, []any{1}, 1, map[string]any{"0": []any{1}}}, []int{1, 2, 3}},
					{`value[0]['name'] == 'x'`, []any{[]any{map[string]any{"name": "x"}}, map[string]any{"0": map[string]any{"name": "x"}}, []any{"x"}}, []int{0}},
					{`value['0'][0] == 1`, []any{map[string]any{"0": []any{1}}, []any{[]any{1}}, 1}, []int{0}},
					{`value['a\\b'] == 'x'`, []any{map[string]any{`a\b`: "x"}, map[string]any{"ab": "x"}}, []int{0}},
				} {
					t.Run(test.source, func(t *testing.T) {
						docs := nativeDocuments(t, test.values)
						predicate := nativePredicate(t, test.source)
						var want []string
						for _, index := range test.want {
							want = append(want, docs[index].ID)
						}
						for _, operation := range []string{"search", "delete"} {
							if err := fixture.install(t.Context(), docs); err != nil {
								t.Fatal(err)
							}
							var got []string
							var err error
							if operation == "search" {
								got, err = fixture.search(t.Context(), predicate, len(docs))
							} else {
								got, err = fixture.delete(t.Context(), docs, predicate)
							}
							slices.Sort(got)
							if err != nil || !slices.Equal(got, want) {
								t.Errorf("%s selected %v, error %v; want %v", operation, got, err, want)
							}
						}
					})
				}
			})
			t.Run("type_errors", func(t *testing.T) { nativeTypeErrors(t, fixture) })
			t.Run("atomic_filter_failure", func(t *testing.T) {
				docs := nativeDocuments(t, []any{7, "7"})
				if err := fixture.install(t.Context(), docs); err != nil {
					t.Fatal(err)
				}
				predicate := nativePredicate(t, `value > 3`)
				calls := fixture.embeddingCalls.Load()
				if _, err := fixture.search(t.Context(), predicate, 1); err == nil || fixture.embeddingCalls.Load() != calls {
					t.Fatalf("search concealed an invalid row beyond TopK: %v", err)
				}
				if err := fixture.store.DeleteWhere(t.Context(), predicate); err == nil {
					t.Fatal("deletion concealed an invalid row")
				}
				ids, err := fixture.ids(t.Context())
				if err != nil || !slices.Equal(ids, []string{docs[0].ID, docs[1].ID}) {
					t.Fatalf("failed deletion changed rows: %v, %v", ids, err)
				}
			})
			t.Run("missing_has", func(t *testing.T) {
				docs := []*document.Document{{ID: "absent", Text: "filter conformance"}, {ID: "empty", Text: "filter conformance", Metadata: metadata.Map{}}}
				predicate := nativePredicate(t, `not (value has 'x')`)
				if err := fixture.install(t.Context(), docs); err != nil {
					t.Fatal(err)
				}
				ids, err := fixture.search(t.Context(), predicate, len(docs))
				slices.Sort(ids)
				if err != nil || !slices.Equal(ids, []string{"absent", "empty"}) {
					t.Fatalf("missing HAS selected %v, error %v", ids, err)
				}
			})
			t.Run("snapshot", func(t *testing.T) { nativeFilterSnapshot(t, fixture) })
			t.Run("embedding_failure", func(t *testing.T) {
				docs := nativeDocuments(t, []any{7})
				if err := fixture.install(t.Context(), docs); err != nil {
					t.Fatal(err)
				}
				failure := errors.New("embedding unavailable")
				fixture.beforeEmbedding = func(context.Context) error { return failure }
				defer func() { fixture.beforeEmbedding = nil }()
				_, err := fixture.search(t.Context(), nativePredicate(t, `value > 3`), 1)
				if !errors.Is(err, failure) || fixture.pool.Stat().AcquiredConns() != 0 {
					t.Fatalf("search = %v, acquired connections = %d", err, fixture.pool.Stat().AcquiredConns())
				}
			})
		})
	}
}

func nativeTypeErrors(t *testing.T, fixture *nativeFixture) {
	t.Helper()
	for _, value := range []any{"7", true, []any{7}, map[string]any{"n": 7}} {
		for _, source := range []string{`value > 3`, `not (value > 3)`, `value > 3 or other == 'x'`, `value > 3 and value == 7`, `value > 3 or value == '7'`, `value == 7 or value > 3`, `value == 7 and value > 3`, `value != 7 or value > 3`} {
			t.Run(fmt.Sprintf("%T/%s", value, source), func(t *testing.T) {
				docs := nativeDocuments(t, []any{value})
				predicate := nativePredicate(t, source)
				values, err := docs[0].Metadata.Values()
				if err != nil {
					t.Fatal(err)
				}
				match, expectedErr := filter.Match(predicate, values)
				for _, operation := range []string{"search", "delete"} {
					if installErr := fixture.install(t.Context(), docs); installErr != nil {
						t.Fatal(installErr)
					}
					calls := fixture.embeddingCalls.Load()
					var got []string
					if operation == "search" {
						got, err = fixture.search(t.Context(), predicate, 1)
					} else {
						got, err = fixture.delete(t.Context(), docs, predicate)
					}
					if expectedErr != nil {
						if err == nil || errors.Unwrap(err) == nil || errors.Unwrap(err).Error() != expectedErr.Error() {
							t.Fatalf("%s error = %v, want wrapped Core error %v", operation, err, expectedErr)
						}
						ids, readErr := fixture.ids(t.Context())
						if readErr != nil || !slices.Equal(ids, []string{docs[0].ID}) || fixture.embeddingCalls.Load() != calls {
							t.Fatalf("%s invalid filter changed effects: IDs %v, read error %v, embedding calls %d -> %d", operation, ids, readErr, calls, fixture.embeddingCalls.Load())
						}
					} else {
						var want []string
						if match {
							want = []string{docs[0].ID}
						}
						if err != nil || !slices.Equal(got, want) {
							t.Fatalf("%s selected %v, error %v; want %v", operation, got, err, want)
						}
					}
					if fixture.pool.Stat().AcquiredConns() != 0 {
						t.Fatal("filter operation retained a transaction connection")
					}
				}
			})
		}
	}
	for _, value := range []any{7, true, []any{"a"}, map[string]any{"s": "a"}} {
		docs := nativeDocuments(t, []any{value})
		if err := fixture.install(t.Context(), docs); err != nil {
			t.Fatal(err)
		}
		calls := fixture.embeddingCalls.Load()
		predicate := nativePredicate(t, `not (value like 'a%')`)
		if _, err := fixture.search(t.Context(), predicate, 1); err == nil || fixture.embeddingCalls.Load() != calls {
			t.Fatalf("LIKE on %T = %v, embedding calls %d -> %d", value, err, calls, fixture.embeddingCalls.Load())
		}
		if err := fixture.store.DeleteWhere(t.Context(), predicate); err == nil {
			t.Fatalf("LIKE deletion on %T succeeded", value)
		}
		ids, err := fixture.ids(t.Context())
		if err != nil || !slices.Equal(ids, []string{docs[0].ID}) {
			t.Fatalf("invalid LIKE deleted document: %v, %v", ids, err)
		}
	}
}

func nativeFilterSnapshot(t *testing.T, fixture *nativeFixture) {
	t.Helper()
	docs := nativeDocuments(t, []any{7})
	if err := fixture.install(t.Context(), docs); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	entered, release := make(chan struct{}, 1), make(chan struct{})
	releaseGate := sync.OnceFunc(func() { close(release) })
	fixture.beforeEmbedding = func(ctx context.Context) error {
		entered <- struct{}{}
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	var workers sync.WaitGroup
	defer func() {
		releaseGate()
		cancel()
		workers.Wait()
		fixture.beforeEmbedding = nil
	}()
	result := make(chan struct {
		response *vectorstore.SearchResponse
		err      error
	}, 1)
	predicate := nativePredicate(t, `value > 3`)
	workers.Go(func() {
		response, err := fixture.store.Search(ctx, &vectorstore.SearchRequest{Query: "filter conformance", Options: vectorstore.SearchOptions{TopK: 1, Filter: predicate}})
		result <- struct {
			response *vectorstore.SearchResponse
			err      error
		}{response, err}
	})
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if _, err := fixture.pool.Exec(ctx, "UPDATE "+fixture.table+" SET facts = $1 WHERE id = $2", []byte(`{"value":"7"}`), []byte(docs[0].ID)); err != nil {
		t.Fatal(err)
	}
	releaseGate()
	select {
	case got := <-result:
		if got.err != nil || got.response == nil || len(got.response.Results) != 1 || !got.response.Results[0].Document.Metadata.Equal(docs[0].Metadata) {
			t.Fatalf("filter snapshot lost its original metadata: response %#v, error %v", got.response, got.err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
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
