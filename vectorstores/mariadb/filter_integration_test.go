//go:build integration

package mariadb_test

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
	"github.com/Tangerg/scope/core/vectorstore/storetest"
	"github.com/Tangerg/scope/vectorstores/mariadb"
)

type nativeFixture struct {
	db              *sql.DB
	store           *mariadb.Store
	schema          string
	table           string
	model           embedding.Model
	embeddingCalls  atomic.Int64
	beforeEmbedding func(context.Context) error
}

func newNativeFixture(t *testing.T) *nativeFixture {
	t.Helper()
	dsn := os.Getenv("SCOPE_MARIADB_DSN")
	if dsn == "" {
		t.Fatal("SCOPE_MARIADB_DSN is required with -tags=integration")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	schema := "scope_filter_" + strings.ToLower(rand.Text())
	f := &nativeFixture{db: db, schema: schema, table: schema + ".documents"}
	f.model = embedding.ModelFunc(func(ctx context.Context, request *embedding.Request) (*embedding.Response, error) {
		f.embeddingCalls.Add(1)
		if f.beforeEmbedding != nil {
			if err := f.beforeEmbedding(ctx); err != nil {
				return nil, err
			}
		}
		outputs := make([]*embedding.Output, len(request.Texts))
		for index := range outputs {
			outputs[index] = &embedding.Output{Embedding: []float64{1, 0}}
		}
		return embedding.NewResponse(outputs, nil)
	})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 30*time.Second)
		defer cancel()
		if _, err := db.ExecContext(ctx, "DROP DATABASE IF EXISTS "+schema); err != nil {
			t.Errorf("cleanup isolated database: %v", err)
		}
	})
	f.store, err = mariadb.NewStore(t.Context(), f.config(true))
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (n *nativeFixture) config(initialize bool) mariadb.StoreConfig {
	return mariadb.StoreConfig{DB: n.db, SchemaName: n.schema, TableName: "documents", MetadataColumn: "facts", EmbeddingModel: n.model, DocumentBatcher: nativeBatcher{}, Dimensions: 2, InitializeSchema: initialize}
}

func (n *nativeFixture) install(ctx context.Context, docs []*document.Document) error {
	if _, err := n.db.ExecContext(ctx, "DELETE FROM "+n.table); err != nil {
		return err
	}
	return n.store.Index(ctx, &vectorstore.IndexRequest{Documents: docs})
}

func (n *nativeFixture) search(ctx context.Context, predicate filter.Predicate, limit int) ([]string, error) {
	response, err := n.store.Search(ctx, &vectorstore.SearchRequest{Query: "filter conformance", Options: vectorstore.SearchOptions{TopK: limit, Filter: predicate}})
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
	rows, err := n.db.QueryContext(ctx, "SELECT id FROM "+n.table+" ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
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

func TestLiveMetadataFilters(t *testing.T) {
	f := newNativeFixture(t)
	for _, operation := range []string{"search", "delete"} {
		t.Run(operation, func(t *testing.T) {
			storetest.FilterConformance(t, storetest.FilterConfig{Query: func(ctx context.Context, docs []*document.Document, predicate filter.Predicate) ([]string, error) {
				if err := f.install(ctx, docs); err != nil {
					return nil, err
				}
				if operation == "delete" {
					return f.delete(ctx, docs, predicate)
				}
				return f.search(ctx, predicate, len(docs))
			}})
		})
	}
	for _, test := range []struct {
		source string
		values []any
		want   []int
	}{
		{`value == 7`, []any{7, "7", true, []any{7}, map[string]any{}, nil}, []int{0}},
		{`value == true`, []any{true, "true", 1, []any{true}, nil}, []int{0}},
		{`value == 'a'`, []any{"a", json.RawMessage(`"\u0061"`), "A", "a "}, []int{0, 1}},
		{`value has 'a'`, []any{[]any{"a"}, []any{json.RawMessage(`"\u0061"`)}, []any{[]any{"a"}}, "a"}, []int{0, 1}},
		{`value has 9007199254740993`, []any{[]any{int64(9007199254740992)}, []any{int64(9007199254740993)}, []any{[]any{int64(9007199254740993)}}}, []int{1}},
		{`value has true`, []any{[]any{true}, []any{"true"}, []any{1}, []any{[]any{true}}}, []int{0}},
		{`value == 1e-40`, []any{json.Number("1e-40"), json.Number("2e-40"), "1e-40"}, []int{0}},
		{`value in (-9223372036854775808,18446744073709551615,0.1)`, []any{int64(-9223372036854775808), uint64(18446744073709551615), 0.1, "0.1"}, []int{0, 1, 2}},
		{`value < 1e-40`, []any{json.Number("-1e400"), 0, json.Number("9e-41"), json.Number("1e-40"), json.Number("2e-40")}, []int{0, 1, 2}},
		{`value > -1e-40`, []any{json.Number("-2e-40"), json.Number("-1e-40"), json.Number("-9e-41"), 0, json.Number("1e400")}, []int{2, 3, 4}},
		{`value >= 18446744073709551615`, []any{json.Number("18446744073709551614"), json.Number("18446744073709551615"), json.Number("18446744073709551616")}, []int{1, 2}},
		{`value like 'a\\%b'`, []any{`a\pathb`, "apathb", "ab"}, []int{0}},
		{`value like 'a!%b'`, []any{"a!pathb", "apathb", "a!b", "a!!b"}, []int{0, 2, 3}},
		{`value like 'a_b'`, []any{"a\x00b", "ab", "a🙂b", "a\nb"}, []int{0, 2, 3}},
		{`value[0][0] == 1`, []any{[]any{[]any{1}}, []any{1}, 1, map[string]any{"0": []any{1}}}, []int{0}},
		{`not (value[0][0] == 1)`, []any{[]any{[]any{1}}, []any{1}, 1, map[string]any{"0": []any{1}}}, []int{1, 2, 3}},
		{`value['a\\b'] == 'x'`, []any{map[string]any{`a\b`: "x"}, map[string]any{"ab": "x"}}, []int{0}},
		{`value['a"b'] == 'x'`, []any{map[string]any{`a"b`: "x"}, map[string]any{"ab": "x"}}, []int{0}},
		{`value[9223372036854775807] is null`, []any{[]any{"x"}, "x", map[string]any{"9223372036854775807": "x"}}, []int{0, 1, 2}},
	} {
		t.Run(test.source, func(t *testing.T) {
			docs := nativeDocuments(t, test.values)
			predicate := nativePredicate(t, test.source)
			var want []string
			for _, index := range test.want {
				want = append(want, docs[index].ID)
			}
			for _, operation := range []string{"search", "delete"} {
				if err := f.install(t.Context(), docs); err != nil {
					t.Fatal(err)
				}
				var got []string
				var err error
				if operation == "search" {
					got, err = f.search(t.Context(), predicate, len(docs))
				} else {
					got, err = f.delete(t.Context(), docs, predicate)
				}
				slices.Sort(got)
				if err != nil || !slices.Equal(got, want) {
					t.Errorf("%s selected %v, error %v; want %v", operation, got, err, want)
				}
			}
		})
	}
	t.Run("type_errors", func(t *testing.T) { nativeTypeErrors(t, f) })
	t.Run("snapshot", func(t *testing.T) { nativeFilterSnapshot(t, f) })
	t.Run("embedding_failure", func(t *testing.T) {
		if err := f.install(t.Context(), nativeDocuments(t, []any{7})); err != nil {
			t.Fatal(err)
		}
		failure := errors.New("embedding unavailable")
		f.beforeEmbedding = func(context.Context) error { return failure }
		defer func() { f.beforeEmbedding = nil }()
		if _, err := f.search(t.Context(), nativePredicate(t, `value > 3`), 1); !errors.Is(err, failure) || f.db.Stats().InUse != 0 {
			t.Fatalf("search failure = %v, in-use connections %d", err, f.db.Stats().InUse)
		}
	})
}

func nativeTypeErrors(t *testing.T, f *nativeFixture) {
	t.Helper()
	for _, value := range []any{"7", true, []any{7}, map[string]any{"n": 7}, json.Number("1e1000001")} {
		for _, source := range []string{`value > 3`, `not (value > 3)`, `value > 3 or other == 'x'`, `value > 3 and value == 7`, `value == 7 or value > 3`, `value == 7 and value > 3`, `value != 7 or value > 3`, `value like 'a%'`} {
			t.Run(fmt.Sprintf("%T/%s", value, source), func(t *testing.T) {
				docs := nativeDocuments(t, []any{value})
				predicate := nativePredicate(t, source)
				values, err := docs[0].Metadata.Values()
				if err != nil {
					t.Fatal(err)
				}
				match, expectedErr := filter.Match(predicate, values)
				for _, operation := range []string{"search", "delete"} {
					if err := f.install(t.Context(), docs); err != nil {
						t.Fatal(err)
					}
					calls := f.embeddingCalls.Load()
					var got []string
					if operation == "search" {
						got, err = f.search(t.Context(), predicate, 1)
					} else {
						got, err = f.delete(t.Context(), docs, predicate)
					}
					if expectedErr != nil {
						if err == nil || errors.Unwrap(err) == nil || errors.Unwrap(err).Error() != expectedErr.Error() {
							t.Fatalf("%s error = %v, want wrapped Core error %v", operation, err, expectedErr)
						}
						ids, readErr := f.ids(t.Context())
						if readErr != nil || !slices.Equal(ids, []string{docs[0].ID}) || f.embeddingCalls.Load() != calls {
							t.Fatalf("%s changed effects: IDs %v, read error %v, embedding calls %d -> %d", operation, ids, readErr, calls, f.embeddingCalls.Load())
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
					if f.db.Stats().InUse != 0 {
						t.Fatal("filter operation retained a transaction connection")
					}
				}
			})
		}
	}
	docs := nativeDocuments(t, []any{7, "7"})
	if err := f.install(t.Context(), docs); err != nil {
		t.Fatal(err)
	}
	calls := f.embeddingCalls.Load()
	predicate := nativePredicate(t, `value > 3`)
	if _, err := f.search(t.Context(), predicate, 1); err == nil || f.embeddingCalls.Load() != calls {
		t.Fatalf("search concealed an invalid row beyond TopK: %v", err)
	}
	if err := f.store.DeleteWhere(t.Context(), predicate); err == nil {
		t.Fatal("deletion concealed an invalid row")
	}
	ids, err := f.ids(t.Context())
	if err != nil || !slices.Equal(ids, []string{docs[0].ID, docs[1].ID}) {
		t.Fatalf("failed deletion changed rows: %v, %v", ids, err)
	}
}

func nativeFilterSnapshot(t *testing.T, f *nativeFixture) {
	t.Helper()
	docs := nativeDocuments(t, []any{7})
	if err := f.install(t.Context(), docs); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	entered, release := make(chan struct{}, 1), make(chan struct{})
	releaseGate := sync.OnceFunc(func() { close(release) })
	f.beforeEmbedding = func(ctx context.Context) error {
		entered <- struct{}{}
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	var workers sync.WaitGroup
	defer func() { releaseGate(); cancel(); workers.Wait(); f.beforeEmbedding = nil }()
	result := make(chan struct {
		response *vectorstore.SearchResponse
		err      error
	}, 1)
	predicate := nativePredicate(t, `value > 3`)
	workers.Go(func() {
		response, err := f.store.Search(ctx, &vectorstore.SearchRequest{Query: "filter conformance", Options: vectorstore.SearchOptions{TopK: 1, Filter: predicate}})
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
	if _, err := f.db.ExecContext(ctx, "UPDATE "+f.table+" SET facts = ? WHERE id = ?", `{"value":"7"}`, docs[0].ID); err != nil {
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

func TestLiveDocumentIdentity(t *testing.T) {
	f := newNativeFixture(t)
	var docs []*document.Document
	for _, id := range []string{"a", "A", "a ", "é", "e", "a\x00b"} {
		docs = append(docs, &document.Document{ID: id, Text: "identity " + id})
	}
	if err := f.install(t.Context(), docs); err != nil {
		t.Fatal(err)
	}
	ids, err := f.ids(t.Context())
	want := []string{"a", "A", "a ", "é", "e", "a\x00b"}
	slices.Sort(want)
	if err != nil || !slices.Equal(ids, want) {
		t.Fatalf("index aliased document IDs: %v, %v; want %v", ids, err, want)
	}
	if err := f.store.DeleteIDs(t.Context(), []string{"A"}); err != nil {
		t.Fatal(err)
	}
	ids, err = f.ids(t.Context())
	want = slices.DeleteFunc(want, func(id string) bool { return id == "A" })
	if err != nil || !slices.Equal(ids, want) {
		t.Fatalf("delete aliased document IDs: %v, %v; want %v", ids, err, want)
	}
}

func TestLiveSearchAfterReplacement(t *testing.T) {
	f := newNativeFixture(t)
	docs := nativeDocuments(t, []any{"a", "b", "c"})
	want := []string{docs[0].ID, docs[1].ID, docs[2].ID}
	for iteration := range 100 {
		if err := f.install(t.Context(), docs); err != nil {
			t.Fatal(err)
		}
		ids, err := f.search(t.Context(), nil, len(docs))
		slices.Sort(ids)
		if err != nil || !slices.Equal(ids, want) {
			stored, readErr := f.ids(t.Context())
			t.Fatalf("replacement %d: search %v, error %v; primary rows %v, read error %v; want %v", iteration, ids, err, stored, readErr, want)
		}
	}
}

func TestLiveSchemaRejectsCompetingIdentity(t *testing.T) {
	for _, test := range []struct{ name, ddl, want string }{
		{"text_ID", "MODIFY id VARCHAR(64) CHARACTER SET latin1 NOT NULL", "VARBINARY(3072)"},
		{"short_ID", "MODIFY id VARBINARY(253) NOT NULL", "VARBINARY(3072)"},
		{"other_unique_fact", "ADD UNIQUE KEY other_identity (content(64))", "entire document ID"},
		{"composite_primary_key", "MODIFY id VARBINARY(64) NOT NULL, DROP PRIMARY KEY, ADD PRIMARY KEY(id, content(1))", "VARBINARY(3072)"},
		{"ID_prefix", "DROP PRIMARY KEY, ADD PRIMARY KEY(id(32))", "entire document ID"},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newNativeFixture(t)
			if _, err := f.db.ExecContext(t.Context(), "ALTER TABLE "+f.table+" "+test.ddl); err != nil {
				t.Fatal(err)
			}
			for _, initialize := range []bool{false, true} {
				if _, err := mariadb.NewStore(t.Context(), f.config(initialize)); err == nil || !strings.Contains(err.Error(), test.want) {
					t.Fatalf("initialize %v: accepted obsolete identity: %v", initialize, err)
				}
			}
			if f.embeddingCalls.Load() != 0 {
				t.Fatal("schema validation invoked embedding")
			}
		})
	}
}

func TestLiveIDLimitAndMetadataRoundTrip(t *testing.T) {
	f := newNativeFixture(t)
	docs := []*document.Document{{ID: strings.Repeat("x", 3072), Text: "max ID", Metadata: nil}, {ID: "empty", Text: "empty metadata", Metadata: metadata.Map{}}}
	if err := f.install(t.Context(), docs); err != nil {
		t.Fatal(err)
	}
	response, err := f.store.Search(t.Context(), &vectorstore.SearchRequest{Query: "round trip", Options: vectorstore.SearchOptions{TopK: 2}})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 2 {
		t.Fatalf("round trip selected %d documents", len(response.Results))
	}
	for _, hit := range response.Results {
		if hit.Document.ID == "empty" && hit.Document.Metadata == nil {
			t.Fatal("empty metadata became nil")
		}
		if hit.Document.ID != "empty" && hit.Document.Metadata != nil {
			t.Fatal("nil metadata became an object")
		}
	}
	calls := f.embeddingCalls.Load()
	invalid := []*document.Document{{ID: "valid", Text: "valid"}, {ID: strings.Repeat("é", 1537), Text: "too long"}}
	if err := f.store.Index(t.Context(), &vectorstore.IndexRequest{Documents: invalid}); !errors.Is(err, vectorstore.ErrInvalidDocument) || f.embeddingCalls.Load() != calls {
		t.Fatalf("oversized ID = %v, embedding calls %d -> %d", err, calls, f.embeddingCalls.Load())
	}
	ids, err := f.ids(t.Context())
	want := []string{"empty", docs[0].ID}
	if err != nil || !slices.Equal(ids, want) {
		t.Fatalf("invalid input changed stored rows: %v, %v", ids, err)
	}
}

func TestLiveDefaultDatabaseAndIdentifierCase(t *testing.T) {
	f := newNativeFixture(t)
	driverConfig, err := mysql.ParseDSN(os.Getenv("SCOPE_MARIADB_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	driverConfig.DBName = f.schema
	connector, err := mysql.NewConnector(driverConfig)
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(connector)
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	config := f.config(false)
	config.DB, config.SchemaName, config.IDColumn = db, "", "ID"
	store, err := mariadb.NewStore(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	docs := nativeDocuments(t, []any{7})
	if err := store.Index(t.Context(), &vectorstore.IndexRequest{Documents: docs}); err != nil {
		t.Fatal(err)
	}
	response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{Filter: nativePredicate(t, `value > 3`)}})
	if err != nil || response == nil || len(response.Results) != 1 || response.Results[0].Document.ID != docs[0].ID {
		t.Fatalf("default database query = %#v, %v", response, err)
	}
}

func TestLiveSchemaRequiresTransactionalStorage(t *testing.T) {
	f := newNativeFixture(t)
	if _, err := f.db.ExecContext(t.Context(), "ALTER TABLE "+f.table+" DROP PRIMARY KEY, ENGINE=MyISAM"); err != nil {
		t.Fatal(err)
	}
	if _, err := mariadb.NewStore(t.Context(), f.config(false)); err == nil || !strings.Contains(err.Error(), "InnoDB") {
		t.Fatalf("non-transactional table accepted: %v", err)
	}
}

func TestLivePagedFilteringAndGlobalRanking(t *testing.T) {
	f := newNativeFixture(t)
	const count = 1030
	docs := make([]*document.Document, count)
	for index := range docs {
		facts, err := metadata.FromValues(map[string]any{"value": index != 0})
		if err != nil {
			t.Fatal(err)
		}
		docs[index] = &document.Document{ID: fmt.Sprintf("doc-%04d", index), Text: strconv.Itoa(index), Metadata: facts}
	}
	config := f.config(false)
	config.DistanceMetric = mariadb.DistanceEuclidean
	config.EmbeddingModel = embedding.ModelFunc(func(_ context.Context, request *embedding.Request) (*embedding.Response, error) {
		outputs := make([]*embedding.Output, len(request.Texts))
		for position, text := range request.Texts {
			distance := 0
			if index, err := strconv.Atoi(text); err == nil {
				distance = 1000 + index
				switch index {
				case 0:
					distance = 0
				case 1:
					distance = 3
				case 600:
					distance = 2
				case 1029:
					distance = 1
				}
			}
			outputs[position] = &embedding.Output{Embedding: []float64{1 + float64(distance), 0}}
		}
		return embedding.NewResponse(outputs, nil)
	})
	var err error
	f.store, err = mariadb.NewStore(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.install(t.Context(), docs); err != nil {
		t.Fatal(err)
	}
	request := &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{TopK: 3, Filter: nativePredicate(t, `value == true`)}}
	response, err := f.store.Search(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	wantIDs, wantScores := []string{"doc-1029", "doc-0600", "doc-0001"}, []vectorstore.Score{0.5, 1.0 / 3, 0.25}
	if len(response.Results) != len(wantIDs) {
		t.Fatalf("global ranking returned %d results", len(response.Results))
	}
	for index, hit := range response.Results {
		if hit.Document.ID != wantIDs[index] || hit.Score != wantScores[index] {
			t.Fatalf("global result %d = %s/%v, want %s/%v", index, hit.Document.ID, hit.Score, wantIDs[index], wantScores[index])
		}
	}
	request.Options.MinScore = 0.3
	response, err = f.store.Search(t.Context(), request)
	if err != nil || response == nil || len(response.Results) != 2 || response.Results[0].Document.ID != wantIDs[0] || response.Results[1].Document.ID != wantIDs[1] {
		t.Fatalf("score threshold = %#v, %v", response, err)
	}
	if err := f.store.DeleteWhere(t.Context(), request.Options.Filter); err != nil {
		t.Fatal(err)
	}
	ids, err := f.ids(t.Context())
	if err != nil || !slices.Equal(ids, []string{"doc-0000"}) {
		t.Fatalf("paged deletion left %v, %v", ids, err)
	}
}

func TestLivePagedValidationAndDeletionAreAtomic(t *testing.T) {
	f := newNativeFixture(t)
	docs := make([]*document.Document, 1030)
	for index := range docs {
		facts, err := metadata.FromValues(map[string]any{"value": 7})
		if err != nil {
			t.Fatal(err)
		}
		docs[index] = &document.Document{ID: fmt.Sprintf("doc-%04d", index), Text: "filter conformance", Metadata: facts}
	}
	if err := docs[len(docs)-1].Metadata.Set("value", "7"); err != nil {
		t.Fatal(err)
	}
	if err := f.install(t.Context(), docs); err != nil {
		t.Fatal(err)
	}
	calls := f.embeddingCalls.Load()
	predicate := nativePredicate(t, `value > 3`)
	if _, err := f.search(t.Context(), predicate, 1); err == nil || f.embeddingCalls.Load() != calls {
		t.Fatalf("last-page invalid metadata was hidden: %v", err)
	}
	if err := f.store.DeleteWhere(t.Context(), predicate); err == nil {
		t.Fatal("last-page invalid metadata allowed deletion")
	}
	var want []string
	for _, doc := range docs {
		want = append(want, doc.ID)
	}
	ids, err := f.ids(t.Context())
	if err != nil || !slices.Equal(ids, want) {
		t.Fatalf("invalid filter changed IDs: %v, %v", ids, err)
	}
	if err := docs[len(docs)-1].Metadata.Set("value", 7); err != nil {
		t.Fatal(err)
	}
	if err := f.install(t.Context(), docs); err != nil {
		t.Fatal(err)
	}
	trigger := "CREATE TRIGGER " + f.schema + ".reject_last_delete BEFORE DELETE ON " + f.table + " FOR EACH ROW BEGIN IF OLD.id = 'doc-1029' THEN SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'last page rejected'; END IF; END"
	if _, err := f.db.ExecContext(t.Context(), trigger); err != nil {
		t.Fatal(err)
	}
	err = f.store.DeleteWhere(t.Context(), predicate)
	var nativeErr *mysql.MySQLError
	if !errors.As(err, &nativeErr) || nativeErr.Number != 1644 {
		t.Fatalf("last-page failure = %v, want native rejection", err)
	}
	ids, err = f.ids(t.Context())
	if err != nil || !slices.Equal(ids, want) {
		t.Fatalf("failed last page committed earlier deletions: %v, %v", ids, err)
	}
	if f.db.Stats().InUse != 0 {
		t.Fatal("failed deletion retained a transaction")
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
