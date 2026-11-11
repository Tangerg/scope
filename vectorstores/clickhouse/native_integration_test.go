//go:build integration

package clickhouse_test

import (
	"context"
	"crypto/rand"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
	"github.com/Tangerg/scope/core/vectorstore/storetest"
	storeclickhouse "github.com/Tangerg/scope/vectorstores/clickhouse"
)

type nativeFixture struct {
	db      *sql.DB
	schema  string
	options *clickhouse.Options
}

func newNativeFixture(t *testing.T) *nativeFixture {
	t.Helper()
	dsn := os.Getenv("SCOPE_CLICKHOUSE_DSN")
	if dsn == "" {
		t.Fatal("SCOPE_CLICKHOUSE_DSN is required with -tags=integration; use an isolated ClickHouse server")
	}
	options, err := clickhouse.ParseDSN(dsn)
	if err != nil {
		t.Fatal(err)
	}
	options.DialTimeout = 10 * time.Second
	options.ReadTimeout = 30 * time.Second
	db := clickhouse.OpenDB(options)
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	f := &nativeFixture{db: db, schema: "scope_native_" + strings.ToLower(rand.Text()), options: options}
	if _, err := db.ExecContext(t.Context(), "CREATE DATABASE "+f.schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 30*time.Second)
		defer cancel()
		if _, err := db.ExecContext(ctx, "DROP DATABASE "+f.schema); err != nil {
			t.Errorf("drop isolated fixture: %v", err)
		}
	})
	return f
}

func (n *nativeFixture) config(metric storeclickhouse.DistanceMetric, initialize bool) storeclickhouse.StoreConfig {
	return storeclickhouse.StoreConfig{
		DB: n.db, DatabaseName: n.schema, Space: testSpace,
		Dimensions: 2, DistanceMetric: metric, InitializeSchema: initialize,
	}
}

const testSpace vectorstore.SpaceID = "native-test-space"

func testBatch(docs []*document.Document) *vectorstore.Batch {
	b := &vectorstore.Batch{Space: testSpace}
	for _, d := range docs {
		v := []float64{1, 0}
		if d.Text == "far" {
			v = []float64{0, 1}
		}
		b.Records = append(b.Records, vectorstore.Record{Document: d, Values: v})
	}
	return b
}

func testScanRequest(p filter.Predicate) vectorstore.ScanRequest {
	return vectorstore.ScanRequest{Filter: p, Budget: testScanBudget()}
}

func testScanBudget() vectorstore.ScanBudget {
	return vectorstore.ScanBudget{MaxRecords: 10000, MaxBytes: 64 << 20, Timeout: time.Minute}
}

func TestNativeStoreSchemaAndDistances(t *testing.T) {
	for _, metric := range []storeclickhouse.DistanceMetric{storeclickhouse.DistanceCosine, storeclickhouse.DistanceL2} {
		t.Run(metric.String(), func(t *testing.T) {
			f := newNativeFixture(t)
			store, err := storeclickhouse.NewStore(t.Context(), f.config(metric, true))
			if err != nil {
				t.Fatalf("construct with native schema initialization: %v", err)
			}
			store, err = storeclickhouse.NewStore(t.Context(), f.config(metric, false))
			if err != nil {
				t.Fatalf("reopen initialized native schema: %v", err)
			}
			nearMetadata, err := metadata.FromValues(map[string]any{"name": "near", "count": 7, "active": true, "note": nil})
			if err != nil {
				t.Fatal(err)
			}
			farMetadata, err := metadata.FromValues(map[string]any{"name": "far"})
			if err != nil {
				t.Fatal(err)
			}
			docs := []*document.Document{
				{ID: "near", Text: "near", Metadata: nearMetadata},
				{ID: "far", Text: "far", Metadata: farMetadata},
			}
			if err := store.Upsert(t.Context(), testBatch(docs)); err != nil {
				t.Fatal(err)
			}
			request := &vectorstore.Query{Space: testSpace, Values: []float64{1, 0}, Options: vectorstore.SearchOptions{TopK: 2}}
			response, err := store.Query(t.Context(), request)
			if err != nil {
				t.Fatalf("read native distance and metadata: %v", err)
			}
			if len(response.Results) != 2 || response.Results[0].Document.ID != "near" || response.Results[1].Document.ID != "far" {
				t.Fatalf("ranked native results = %+v", response.Results)
			}
			if !response.Results[0].Document.Metadata.Equal(nearMetadata) || !response.Results[1].Document.Metadata.Equal(farMetadata) {
				t.Fatal("native result metadata differs from the indexed metadata")
			}
			wantFarScore := 0.5
			if metric == storeclickhouse.DistanceL2 {
				wantFarScore = 1 / (1 + math.Sqrt(2))
			}
			if response.Results[0].Score != 1 || math.Abs(response.Results[1].Score.Float64()-wantFarScore) > 1e-6 {
				t.Fatalf("native scores = %v, %v; want 1, %v", response.Results[0].Score, response.Results[1].Score, wantFarScore)
			}
			if err := store.DeleteIDs(t.Context(), []string{"near"}); err != nil {
				t.Fatal(err)
			}
			response, err = store.Query(t.Context(), request)
			if err != nil || len(response.Results) != 1 || response.Results[0].Document.ID != "far" {
				t.Fatalf("native results after ID deletion = %+v, %v", response, err)
			}
			if err := store.DeleteMatching(t.Context(), filter.EQ("name", "far"), testScanBudget()); err != nil {
				t.Fatal(err)
			}
			response, err = store.Query(t.Context(), request)
			if err != nil || len(response.Results) != 0 {
				t.Fatalf("native results after filtered deletion = %+v, %v", response, err)
			}
		})
	}
}

func (n *nativeFixture) newStore(t *testing.T) *storeclickhouse.Store {
	t.Helper()
	store, err := storeclickhouse.NewStore(t.Context(), n.config(storeclickhouse.DistanceCosine, true))
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func (n *nativeFixture) install(ctx context.Context, store *storeclickhouse.Store, docs []*document.Document) error {
	if _, err := n.db.ExecContext(ctx, "TRUNCATE TABLE "+n.schema+"."+testSpace.StorageKey()+"_vector_store"); err != nil {
		return err
	}
	return store.Upsert(ctx, testBatch(docs))
}

func TestNativeCoreFilterConformance(t *testing.T) {
	f := newNativeFixture(t)
	store := f.newStore(t)
	storetest.FilterConformance(t, storetest.FilterConfig{Query: func(ctx context.Context, docs []*document.Document, predicate filter.Predicate) ([]string, error) {
		if err := f.install(ctx, store, docs); err != nil {
			return nil, err
		}
		response, err := store.Scan(ctx, testScanRequest(predicate))
		if err != nil {
			return nil, err
		}
		var ids []string
		for _, result := range response {
			ids = append(ids, result.ID)
		}
		return ids, nil
	}})
}

func TestNativeCoreDeleteConformance(t *testing.T) {
	f := newNativeFixture(t)
	store := f.newStore(t)
	storetest.FilterConformance(t, storetest.FilterConfig{Query: func(ctx context.Context, docs []*document.Document, predicate filter.Predicate) ([]string, error) {
		if err := f.install(ctx, store, docs); err != nil {
			return nil, err
		}
		if err := store.DeleteMatching(ctx, predicate, testScanBudget()); err != nil {
			return nil, err
		}
		response, err := store.Query(ctx, &vectorstore.Query{Space: testSpace, Values: []float64{1, 0}, Options: vectorstore.SearchOptions{TopK: len(docs)}})
		if err != nil {
			return nil, err
		}
		remaining := make(map[string]bool, len(response.Results))
		for _, result := range response.Results {
			remaining[result.Document.ID] = true
		}
		var deleted []string
		for _, doc := range docs {
			if !remaining[doc.ID] {
				deleted = append(deleted, doc.ID)
			}
		}
		return deleted, nil
	}})
}

func TestNativeEncodedFactsAndExactIdentities(t *testing.T) {
	f := newNativeFixture(t)
	store := f.newStore(t)
	ids := []string{"ID", "id", "id\x00", "id ", "\x00head", "中文", "zz_" + strings.Repeat("x", 300_000)}
	facts := metadata.Map{"huge": json.RawMessage(`1e400`), "tiny": json.RawMessage(`1e-400`), "escaped": json.RawMessage(`"\u0041lice"`), "nested": json.RawMessage(`{"items":[1e400]}`)}
	docs := make([]*document.Document, len(ids))
	for i, id := range ids {
		m := facts
		if i == 0 {
			m = nil
		}
		if i == 1 {
			m = metadata.Map{}
		}
		docs[i] = &document.Document{ID: id, Text: "text", Metadata: m}
	}
	if err := store.Upsert(t.Context(), testBatch(docs)); err != nil {
		t.Fatal(err)
	}
	request := &vectorstore.Query{Space: testSpace, Values: []float64{1, 0}, Options: vectorstore.SearchOptions{TopK: len(docs)}}
	response, err := store.Query(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != len(docs) {
		t.Fatalf("native IDs collapsed: count %d", len(response.Results))
	}
	ordered := slices.Clone(ids)
	slices.Sort(ordered)
	byID := make(map[string]*document.Document, len(docs))
	for _, doc := range docs {
		byID[doc.ID] = doc
	}
	for i, result := range response.Results {
		if result.Document.ID != ordered[i] {
			t.Fatalf("byte identity order differs at %d", i)
		}
		source := byID[result.Document.ID].Metadata
		encoded, err := source.MarshalJSON()
		if err != nil {
			t.Fatal(err)
		}
		var projected metadata.Map
		if err := projected.UnmarshalJSON(encoded); err != nil {
			t.Fatal(err)
		}
		if (source == nil) != (result.Document.Metadata == nil) || !projected.Equal(result.Document.Metadata) {
			t.Fatalf("Core encoded fact changed for ID position %d", i)
		}
	}
	for _, src := range []string{`huge > 1`, `tiny > 0`, `nested['items'][0] > 1`, `escaped == 'Alice'`} {
		predicate, err := filter.Parse(src)
		if err != nil {
			t.Fatal(err)
		}
		response, err := store.Scan(t.Context(), testScanRequest(predicate))
		if err != nil {
			t.Fatal(err)
		}
		if len(response) != len(docs)-2 {
			t.Fatalf("%s selected %d documents", src, len(response))
		}
	}
	if err := store.DeleteIDs(t.Context(), []string{ids[2], ids[6]}); err != nil {
		t.Fatal(err)
	}
	response, err = store.Query(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != len(docs)-2 {
		t.Fatal("exact identity deletion count differs")
	}
	for _, r := range response.Results {
		if r.Document.ID == ids[2] || r.Document.ID == ids[6] {
			t.Fatal("typed ID parameters did not delete exact bytes")
		}
	}
}

func TestNativeFilterPreflightRejectsLateFailure(t *testing.T) {
	f := newNativeFixture(t)
	store := f.newStore(t)
	docs := make([]*document.Document, 1030)
	for i := range docs {
		m, err := metadata.FromValues(map[string]any{"n": 7})
		if err != nil {
			t.Fatal(err)
		}
		docs[i] = &document.Document{ID: fmt.Sprintf("%08d", i), Text: "text", Metadata: m}
	}
	if err := docs[len(docs)-1].Metadata.Set("n", "wrong"); err != nil {
		t.Fatal(err)
	}
	if err := store.Upsert(t.Context(), testBatch(docs)); err != nil {
		t.Fatal(err)
	}
	response, err := store.Scan(t.Context(), testScanRequest(filter.GT("n", 1)))
	if err == nil || response != nil {
		t.Fatal("late type mismatch became a successful limited search")
	}
	if err := store.DeleteMatching(t.Context(), filter.GT("n", 1), testScanBudget()); err == nil {
		t.Fatal("late type mismatch became a successful deletion")
	}
	var count uint64
	if err := f.db.QueryRowContext(t.Context(), "SELECT count() FROM "+f.schema+"."+testSpace.StorageKey()+"_vector_store FINAL").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != uint64(len(docs)) {
		t.Fatalf("failure published partial deletion: %d", count)
	}
}

func TestNativeCallerOptionsCannotAdvanceRequestFacts(t *testing.T) {
	f := newNativeFixture(t)
	store := f.newStore(t)
	docs := []*document.Document{{ID: "near", Text: "near"}, {ID: "far", Text: "far"}}
	if err := store.Upsert(t.Context(), testBatch(docs)); err != nil {
		t.Fatal(err)
	}
	ctx := clickhouse.Context(t.Context(), clickhouse.WithParameters(clickhouse.Parameters{"scope_vector": "[0,1]", "scope_limit": "1", "scope_ids": "['far']"}), clickhouse.WithAsync(false), clickhouse.WithSettings(clickhouse.Settings{"implicit_transaction": false, "result_overflow_mode": "break", "max_result_rows": 1}))
	response, err := store.Query(ctx, &vectorstore.Query{Space: testSpace, Values: []float64{1, 0}, Options: vectorstore.SearchOptions{TopK: 2}})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 2 || response.Results[0].Document.ID != "near" {
		t.Fatal("caller context replaced computed vector or TopK")
	}
	if err := store.DeleteIDs(ctx, []string{"near"}); err != nil {
		t.Fatal(err)
	}
	response, err = store.Query(t.Context(), &vectorstore.Query{Space: testSpace, Values: []float64{1, 0}, Options: vectorstore.SearchOptions{TopK: 2}})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 1 || response.Results[0].Document.ID != "far" {
		t.Fatal("caller context replaced deletion IDs")
	}
}

func TestNativeScanKeepsSnapshotAcrossPages(t *testing.T) {
	f := newNativeFixture(t)
	store := f.newStore(t)
	docs := make([]*document.Document, 513)
	for i := range docs {
		docs[i] = &document.Document{ID: fmt.Sprintf("%05d", i), Text: "old"}
	}
	if err := store.Upsert(t.Context(), testBatch(docs)); err != nil {
		t.Fatal(err)
	}
	changed := false
	reader := f.faultStore(t, func(ctx context.Context, stmt string) error {
		if changed || !strings.Contains(stmt, "WHERE id > {scope_cursor:String}") {
			return nil
		}
		changed = true
		return store.Upsert(ctx, testBatch([]*document.Document{{ID: docs[512].ID, Text: "new"}}))
	})
	snap, err := reader.Scan(t.Context(), testScanRequest(nil))
	if err != nil || !changed || len(snap) != 513 || snap[512].Text != "old" {
		t.Fatalf("snapshot: count=%d changed=%v err=%v", len(snap), changed, err)
	}
	now, err := store.Scan(t.Context(), testScanRequest(nil))
	if err != nil || len(now) != 513 || now[512].Text != "new" {
		t.Fatalf("fresh snapshot: count=%d err=%v", len(now), err)
	}
}

type faultConnector struct {
	inner      driver.Connector
	beforeExec func(context.Context, string) error
}

func (f *faultConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := f.inner.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &faultConnection{Conn: conn, beforeExec: f.beforeExec}, nil
}

func (f *faultConnector) Driver() driver.Driver { return f.inner.Driver() }

type faultConnection struct {
	driver.Conn
	beforeExec func(context.Context, string) error
}

func (f *faultConnection) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if err := f.beforeExec(ctx, query); err != nil {
		return nil, err
	}
	return f.Conn.(driver.ExecerContext).ExecContext(ctx, query, args)
}

func (f *faultConnection) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if err := f.beforeExec(ctx, query); err != nil {
		return nil, err
	}
	return f.Conn.(driver.QueryerContext).QueryContext(ctx, query, args)
}

func (f *faultConnection) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	return f.Conn.(driver.ConnPrepareContext).PrepareContext(ctx, query)
}

func (f *faultConnection) BeginTx(ctx context.Context, options driver.TxOptions) (driver.Tx, error) {
	return f.Conn.(driver.ConnBeginTx).BeginTx(ctx, options)
}

func (f *faultConnection) CheckNamedValue(value *driver.NamedValue) error {
	return f.Conn.(driver.NamedValueChecker).CheckNamedValue(value)
}

func (n *nativeFixture) faultStore(t *testing.T, beforeExec func(context.Context, string) error) *storeclickhouse.Store {
	t.Helper()
	db := sql.OpenDB(&faultConnector{inner: clickhouse.Connector(n.options), beforeExec: beforeExec})
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	config := n.config(storeclickhouse.DistanceCosine, false)
	config.DB = db
	store, err := storeclickhouse.NewStore(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestNativeDeleteFailureCannotPublishEarlierPages(t *testing.T) {
	f := newNativeFixture(t)
	store := f.newStore(t)
	docs := make([]*document.Document, 1030)
	for i := range docs {
		m, err := metadata.FromValues(map[string]any{"active": true})
		if err != nil {
			t.Fatal(err)
		}
		docs[i] = &document.Document{ID: fmt.Sprintf("%08d", i), Text: "text", Metadata: m}
	}
	if err := store.Upsert(t.Context(), testBatch(docs)); err != nil {
		t.Fatal(err)
	}
	injected := errors.New("native transport failure before later deletion")
	deletes := 0
	broken := f.faultStore(t, func(ctx context.Context, query string) error {
		if !strings.HasPrefix(query, "DELETE FROM ") {
			return nil
		}
		deletes++
		if deletes != 2 {
			return nil
		}
		response, err := store.Query(ctx, &vectorstore.Query{Space: testSpace, Values: []float64{1, 0}, Options: vectorstore.SearchOptions{TopK: len(docs)}})
		if err != nil {
			return err
		}
		if len(response.Results) != len(docs) {
			t.Errorf("uncommitted first page became visible: %d", len(response.Results))
		}
		return injected
	})
	if err := broken.DeleteMatching(t.Context(), filter.EQ("active", true), testScanBudget()); !errors.Is(err, injected) {
		t.Fatal(err)
	}
	var count uint64
	if err := f.db.QueryRowContext(t.Context(), "SELECT count() FROM "+f.schema+"."+testSpace.StorageKey()+"_vector_store FINAL").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != uint64(len(docs)) {
		t.Fatalf("rollback left %d of %d documents", count, len(docs))
	}
}

func TestNativeDeletionPreservesConcurrentReplacement(t *testing.T) {
	f := newNativeFixture(t)
	store := f.newStore(t)
	docs := make([]*document.Document, 1030)
	for i := range docs {
		m, err := metadata.FromValues(map[string]any{"active": true})
		if err != nil {
			t.Fatal(err)
		}
		docs[i] = &document.Document{ID: fmt.Sprintf("%08d", i), Text: "before", Metadata: m}
	}
	if err := store.Upsert(t.Context(), testBatch(docs)); err != nil {
		t.Fatal(err)
	}
	replaced := false
	deleter := f.faultStore(t, func(ctx context.Context, query string) error {
		if replaced || !strings.HasPrefix(query, "DELETE FROM ") {
			return nil
		}
		replaced = true
		updated, err := metadata.FromValues(map[string]any{"active": false})
		if err != nil {
			return err
		}
		return store.Upsert(ctx, testBatch([]*document.Document{{ID: docs[0].ID, Text: "after", Metadata: updated}}))
	})
	if err := deleter.DeleteMatching(t.Context(), filter.EQ("active", true), testScanBudget()); err != nil {
		t.Fatal(err)
	}
	response, err := store.Query(t.Context(), &vectorstore.Query{Space: testSpace, Values: []float64{1, 0}, Options: vectorstore.SearchOptions{TopK: len(docs)}})
	if err != nil {
		t.Fatal(err)
	}
	if !replaced || len(response.Results) != 1 || response.Results[0].Document.ID != docs[0].ID || response.Results[0].Document.Text != "after" {
		t.Fatalf("deletion damaged concurrent replacement: %d results", len(response.Results))
	}
}

func TestNativeHostSettingsCannotResurrectDeletedDocuments(t *testing.T) {
	f := newNativeFixture(t)
	store := f.newStore(t)
	if err := store.Upsert(t.Context(), testBatch([]*document.Document{{ID: "near", Text: "near"}, {ID: "far", Text: "far"}})); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteIDs(t.Context(), []string{"near"}); err != nil {
		t.Fatal(err)
	}
	options := *f.options
	options.Settings = clickhouse.Settings{"apply_deleted_mask": false, "implicit_transaction": false, "async_insert": true, "result_overflow_mode": "break"}
	db := clickhouse.OpenDB(&options)
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	config := f.config(storeclickhouse.DistanceCosine, false)
	config.DB = db
	reader, err := storeclickhouse.NewStore(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	response, err := reader.Query(t.Context(), &vectorstore.Query{Space: testSpace, Values: []float64{1, 0}, Options: vectorstore.SearchOptions{TopK: 2}})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 1 || response.Results[0].Document.ID != "far" {
		t.Fatal("host database settings resurrected a deleted document")
	}
}
