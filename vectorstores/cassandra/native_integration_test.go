//go:build integration

package cassandra

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	gocql "github.com/apache/cassandra-gocql-driver/v2"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
	"github.com/Tangerg/scope/core/vectorstore/storetest"
)

type nativeFixture struct {
	session  *gocql.Session
	keyspace string

	beforeQuery func(string, []any)
	timestamp   int64
}

func newNativeFixture(t *testing.T) *nativeFixture {
	t.Helper()
	host, portString := os.Getenv("SCOPE_CASSANDRA_HOST"), os.Getenv("SCOPE_CASSANDRA_PORT")
	if host == "" || portString == "" {
		t.Fatal("SCOPE_CASSANDRA_HOST and SCOPE_CASSANDRA_PORT are required with -tags=integration; use an isolated Cassandra 5.0+ server")
	}
	port, err := strconv.Atoi(portString)
	if err != nil {
		t.Fatal(err)
	}
	cluster := gocql.NewCluster(host)
	cluster.Port = port
	cluster.DisableInitialHostLookup = true
	cluster.NumConns = 1
	cluster.Timeout = 5 * time.Second
	cluster.ConnectTimeout = 5 * time.Second
	cluster.Consistency = gocql.One
	cluster.PageSize = 7
	session, err := cluster.CreateSession()
	if err != nil {
		t.Fatal(err)
	}
	f := &nativeFixture{session: session, keyspace: "scope_native_" + strings.ToLower(rand.Text())}
	t.Cleanup(func() { session.Close() })
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := session.Query("DROP KEYSPACE IF EXISTS " + quoteIdentifier(f.keyspace)).ExecContext(ctx); err != nil {
			t.Error(err)
		}
	})

	return f
}

func fixtureVector(text string) []float64 {
	vector := []float64{1, 0}
	switch text {
	case "far":
		vector = []float64{0, 1}
	case "big":
		vector = []float64{100, 0}
	case "huge":
		vector = []float64{1e20, 0}
	case "big query":
		vector = []float64{1000, 0}
	case "zero":
		vector = []float64{0, 0}
	case "overflow":
		vector = []float64{math.MaxFloat64, 0}
	case "different width":
		vector = []float64{1, 0, 0}
	}
	return vector
}

func (n *nativeFixture) Query(statement string, values ...any) *gocql.Query {
	if n.beforeQuery != nil {
		n.beforeQuery(statement, values)
	}
	query := n.session.Query(statement, values...)
	if n.timestamp != 0 && (strings.HasPrefix(statement, "INSERT INTO ") || strings.HasPrefix(statement, "UPDATE ") || strings.HasPrefix(statement, "DELETE FROM ")) {
		query.WithTimestamp(n.timestamp)
	}
	return query
}

func (n *nativeFixture) config(metric SimilarityFunction, create bool) StoreConfig {
	dimensions := 0
	if create {
		dimensions = 2
	}
	return StoreConfig{Session: n, KeyspaceName: n.keyspace, TableName: "docs", Space: fixtureSpace, Similarity: metric, CreateDimensions: dimensions}
}

func (n *nativeFixture) store(t *testing.T, metric SimilarityFunction) *Store {
	t.Helper()
	store, err := NewStore(t.Context(), n.config(metric, true))
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func (n *nativeFixture) table() string { return quoteIdentifier(n.keyspace) + `."docs"` }

func (n *nativeFixture) clear(t *testing.T) {
	t.Helper()
	if err := n.session.Query("TRUNCATE " + n.table()).ExecContext(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func (n *nativeFixture) ids(t *testing.T) []string {
	t.Helper()
	iter := n.session.Query("SELECT id FROM " + n.table()).IterContext(t.Context())
	var ids []string
	var id string
	for iter.Scan(&id) {
		ids = append(ids, id)
	}
	if err := iter.Close(); err != nil {
		t.Fatal(err)
	}
	slices.Sort(ids)
	return ids
}

type nativeBatcher struct{}

func (nativeBatcher) Batch(_ context.Context, docs []*document.Document) ([][]*document.Document, error) {
	var out [][]*document.Document
	for chunk := range slices.Chunk(docs, 16) {
		out = append(out, chunk)
	}
	return out, nil
}

func nativeIndex(t *testing.T, store *Store, docs ...*document.Document) {
	t.Helper()
	if err := store.Upsert(t.Context(), fixtureBatch(docs)); err != nil {
		t.Fatal(err)
	}
}

func nativePredicate(t *testing.T, source string) filter.Predicate {
	t.Helper()
	predicate, err := filter.Parse(source)
	if err != nil {
		t.Fatal(err)
	}
	return predicate
}

func TestNativeFilterConformance(t *testing.T) {
	f := newNativeFixture(t)
	store := f.store(t, SimilarityCosine)
	for _, operation := range []string{"search", "delete"} {
		t.Run(operation, func(t *testing.T) {
			storetest.FilterConformance(t, storetest.FilterConfig{Query: func(ctx context.Context, docs []*document.Document, predicate filter.Predicate) ([]string, error) {
				f.clear(t)
				if err := store.Upsert(ctx, fixtureBatch(docs)); err != nil {
					return nil, err
				}
				if operation == "search" {
					response, err := store.QueryByScan(ctx, &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("query"), Options: vectorstore.SearchOptions{TopK: len(docs), Filter: predicate}}, fixtureBudget)
					if err != nil {
						return nil, err
					}
					ids := make([]string, len(response.Results))
					for i, r := range response.Results {
						ids[i] = r.Document.ID
					}
					return ids, nil
				}
				if err := store.DeleteMatching(ctx, predicate, fixtureBudget); err != nil {
					return nil, err
				}
				remaining := f.ids(t)
				var deleted []string
				for _, doc := range docs {
					if !slices.Contains(remaining, doc.ID) {
						deleted = append(deleted, doc.ID)
					}
				}
				return deleted, nil
			}})
		})
	}
}

func TestNativeCanonicalMetadataAndTopK(t *testing.T) {
	for _, metric := range []SimilarityFunction{SimilarityCosine, SimilarityEuclidean, SimilarityDotProduct} {
		t.Run(metric.String(), func(t *testing.T) {
			f := newNativeFixture(t)
			store := f.store(t, metric)
			docs := []*document.Document{{ID: "nil", Text: "query"}, {ID: "empty", Text: "query", Metadata: metadata.Map{}}, {ID: "original/路径'", Text: "far", Metadata: metadata.Map{"ok": json.RawMessage(`true`), "value": json.RawMessage(`9007199254740993`), "null": json.RawMessage(`null`), "nested": json.RawMessage(`{"array":[null,1e1000]}`), "precise": json.RawMessage(`1.00000000000000001`)}}, {ID: "closer", Text: "query", Metadata: metadata.Map{"ok": json.RawMessage(`false`)}}}
			nativeIndex(t, store, docs...)
			response, err := store.QueryByScan(t.Context(), &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("query"), Options: vectorstore.SearchOptions{TopK: len(docs)}}, fixtureBudget)
			if err != nil {
				t.Fatal(err)
			}
			if len(response.Results) != len(docs) {
				t.Fatal(response)
			}
			for _, result := range response.Results {
				source := docs[slices.IndexFunc(docs, func(d *document.Document) bool { return d.ID == result.Document.ID })]
				if !source.Metadata.Equal(result.Document.Metadata) || (source.Metadata == nil) != (result.Document.Metadata == nil) {
					t.Fatalf("roundtrip=%s want %s", result.Document.Metadata, source.Metadata)
				}
			}
			if response.Results[0].Document.ID != "closer" {
				t.Fatal("equal scores must use ID order")
			}
			response, err = store.QueryByScan(t.Context(), &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("query"), Options: vectorstore.SearchOptions{TopK: 1, Filter: nativePredicate(t, "ok == true")}}, fixtureBudget)
			if err != nil || len(response.Results) != 1 || response.First().Document.ID != "original/路径'" {
				t.Fatalf("filtered response=%v err=%v", response, err)
			}
			if err = store.DeleteMatching(t.Context(), nativePredicate(t, "value == 9007199254740993"), fixtureBudget); err != nil {
				t.Fatal(err)
			}
			if got := f.ids(t); len(got) != 3 || slices.Contains(got, "original/路径'") {
				t.Fatal(got)
			}
		})
	}
}

func TestNativePredicatePreflightBeforeThresholdAndDeletion(t *testing.T) {
	f := newNativeFixture(t)
	store := f.store(t, SimilarityCosine)
	docs := make([]*document.Document, 40)
	for i := range docs {
		docs[i] = &document.Document{ID: fmt.Sprintf("doc-%03d", i), Text: "query", Metadata: metadata.Map{"rank": json.RawMessage(`1`)}}
	}
	docs[39].Metadata["rank"] = json.RawMessage(`"wrong type"`)
	nativeIndex(t, store, docs...)
	predicate := nativePredicate(t, "rank < 2")
	response, err := store.QueryByScan(t.Context(), &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("query"), Options: vectorstore.SearchOptions{TopK: 1, MinScore: 1, Filter: predicate}}, fixtureBudget)
	if response != nil || err == nil {
		t.Fatalf("response=%v err=%v ", response, err)
	}
	if err = store.DeleteMatching(t.Context(), predicate, fixtureBudget); err == nil {
		t.Fatal("accepted wrong-type predicate")
	}
	if len(f.ids(t)) != 40 {
		t.Fatal("preflight deleted rows")
	}
	nativeIndex(t, store, &document.Document{ID: "doc-039", Text: "query", Metadata: metadata.Map{"rank": json.RawMessage(`1`)}})
	if err = store.DeleteMatching(t.Context(), predicate, fixtureBudget); err != nil {
		t.Fatal(err)
	}
	if len(f.ids(t)) != 0 {
		t.Fatal("paged delete left rows")
	}
}

func TestNativeCASPreservesConcurrentReplacement(t *testing.T) {
	f := newNativeFixture(t)
	store := f.store(t, SimilarityCosine)
	nativeIndex(t, store, &document.Document{ID: "same", Text: "query", Metadata: metadata.Map{"value": json.RawMessage(`"before"`)}})
	changed := false
	f.beforeQuery = func(statement string, _ []any) {
		if strings.HasPrefix(statement, "DELETE FROM ") && strings.Contains(statement, " IF ") {
			changed = true
			f.beforeQuery = nil
			if err := store.Upsert(t.Context(), fixtureBatch([]*document.Document{{ID: "same", Text: "replacement", Metadata: metadata.Map{"value": json.RawMessage(`"after"`)}}})); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := store.DeleteMatching(t.Context(), nativePredicate(t, "value == 'before'"), fixtureBudget); err == nil || !strings.Contains(err.Error(), "changed during filter deletion") || !changed {
		t.Fatalf("CAS err=%v changed=%v", err, changed)
	}
	var content, raw string
	if err := f.session.Query("SELECT content,metadata FROM "+f.table()+" WHERE id=?", "same").ScanContext(t.Context(), &content, &raw); err != nil {
		t.Fatal(err)
	}
	if content != "replacement" || raw != `{"value":"after"}` {
		t.Fatalf("replacement=%s metadata=%s", content, raw)
	}
}

func TestNativeSearchUsesCapturedVectorsAndMetadata(t *testing.T) {
	f := newNativeFixture(t)
	store := f.store(t, SimilarityCosine)
	nativeIndex(t, store, &document.Document{ID: "same", Text: "query", Metadata: metadata.Map{"value": json.RawMessage(`"before"`)}})
	// The scan captures the row before scoring. Replace the source after the
	// iterator has read it, at the final scalar similarity call.
	scoreCalls := 0
	f.beforeQuery = func(statement string, _ []any) {
		if strings.HasPrefix(statement, "SELECT similarity_") {
			scoreCalls++
			if scoreCalls == 3 {
				f.beforeQuery = nil
				if err := store.Upsert(t.Context(), fixtureBatch([]*document.Document{{ID: "same", Text: "far", Metadata: metadata.Map{"value": json.RawMessage(`"after"`)}}})); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	response, err := store.QueryByScan(t.Context(), &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("query")}, fixtureBudget)
	if err != nil || response.First() == nil || response.First().Document.Text != "query" || response.First().Score != 1 || string(response.First().Document.Metadata["value"]) != `"before"` {
		t.Fatalf("captured: %v %v", response, err)
	}
	response, err = store.QueryByScan(t.Context(), &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("query")}, fixtureBudget)
	if err != nil || response.First().Score != .5 || response.First().Document.Text != "far" {
		t.Fatalf("current response=%v err=%v", response, err)
	}
}

func TestNativeDotRetainsRawRanking(t *testing.T) {
	f := newNativeFixture(t)
	store := f.store(t, SimilarityDotProduct)
	nativeIndex(t, store, &document.Document{ID: "a-lower", Text: "query"}, &document.Document{ID: "z-higher", Text: "big"})
	response, err := store.QueryByScan(t.Context(), &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("big query"), Options: vectorstore.SearchOptions{TopK: 1}}, fixtureBudget)
	if err != nil || response.First() == nil || response.First().Document.ID != "z-higher" || response.First().Score != 1 {
		t.Fatalf("saturated response=%v err=%v", response, err)
	}
	nativeIndex(t, store, &document.Document{ID: "huge", Text: "huge"})
	response, err = store.QueryByScan(t.Context(), &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("query"), Options: vectorstore.SearchOptions{TopK: 1}}, fixtureBudget)
	if err != nil || response.First().Document.ID != "huge" {
		t.Fatalf("large finite dot response=%v err=%v", response, err)
	}
}

func TestNativeStrictMetadataAndVectorPreflight(t *testing.T) {
	f := newNativeFixture(t)
	store := f.store(t, SimilarityCosine)
	for _, raw := range []string{"", `[]`, `{"a":1,"a":2}`} {
		f.clear(t)
		nativeIndex(t, store, &document.Document{ID: "bad", Text: "query"})
		if applied, err := f.session.Query("UPDATE "+f.table()+" SET metadata=? WHERE id=? IF EXISTS", raw, "bad").Consistency(gocql.Quorum).SerialConsistency(gocql.Serial).DefaultTimestamp(false).MapScanCASContext(t.Context(), map[string]any{}); err != nil || !applied {
			t.Fatalf("native corruption injection: applied=%t, %v", applied, err)
		}
		response, err := store.QueryByScan(t.Context(), &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("query"), Options: vectorstore.SearchOptions{Filter: nativePredicate(t, "a == 'hidden'"), MinScore: 1}}, fixtureBudget)
		if response != nil || err == nil {
			t.Fatalf("malformed response=%v err=%v", response, err)
		}
		if err = store.DeleteMatching(t.Context(), nativePredicate(t, "a == 'hidden'"), fixtureBudget); err == nil {
			t.Fatal("accepted malformed record")
		}
		if len(f.ids(t)) != 1 {
			t.Fatal("malformed preflight deleted row")
		}
	}
	f.clear(t)
	for _, text := range []string{"zero", "overflow", "different width"} {
		if err := store.Upsert(t.Context(), fixtureBatch([]*document.Document{{ID: "bad", Text: text}})); err == nil {
			t.Fatal("accepted invalid vector", text)
		}
		if len(f.ids(t)) != 0 {
			t.Fatal("invalid vector published")
		}
	}
}

func TestNativeSchemaAndCancellation(t *testing.T) {
	f := newNativeFixture(t)
	if store, err := NewStore(t.Context(), f.config(SimilarityCosine, false)); store != nil || err == nil {
		t.Fatalf("missing schema store=%v err=%v", store, err)
	}
	store := f.store(t, SimilarityCosine)
	if _, err := NewStore(t.Context(), f.config(SimilarityCosine, false)); err != nil {
		t.Fatal(err)
	}
	creation := f.config(SimilarityCosine, true)
	creation.CreateDimensions = 4
	reopened, err := NewStore(t.Context(), creation)
	if err != nil {
		t.Fatal(err)
	}
	nativeIndex(t, reopened, &document.Document{ID: "native-width", Text: "query"})
	if err = reopened.DeleteIDs(t.Context(), []string{"native-width"}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	response, err := store.QueryByScan(ctx, &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("query")}, fixtureBudget)
	if response != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled response=%v err=%v", response, err)
	}
	if err = store.DeleteIDs(ctx, []string{"missing"}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	nativeIndex(t, store, &document.Document{ID: "same", Text: "query"})
	if err := store.DeleteIDs(t.Context(), []string{"same", "unknown"}); err != nil {
		t.Fatal(err)
	}
	if len(f.ids(t)) != 0 {
		t.Fatal("ID delete left row")
	}
	if err := f.session.Query("ALTER TABLE " + f.table() + " ADD tenant text").ExecContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if store, err := NewStore(t.Context(), f.config(SimilarityCosine, false)); store != nil || err == nil {
		t.Fatalf("old schema store=%v err=%v", store, err)
	}
}

func TestNativeCustomColumnsAndSchemaRejection(t *testing.T) {
	f := newNativeFixture(t)
	config := f.config(SimilarityEuclidean, true)
	config.TableName = "CustomDocs"
	config.IDColumn = "DocumentID"
	config.ContentColumn = "Content"
	config.MetadataColumn = "Facts"
	config.EmbeddingColumn = "Vector"
	store, err := NewStore(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	nativeIndex(t, store, &document.Document{ID: "original/路径", Text: "query", Metadata: metadata.Map{"value": json.RawMessage(`9007199254740993`)}})
	response, err := store.QueryByScan(t.Context(), &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("query"), Options: vectorstore.SearchOptions{Filter: nativePredicate(t, "value == 9007199254740993")}}, fixtureBudget)
	if err != nil || response.First() == nil || response.First().Document.ID != "original/路径" {
		t.Fatalf("custom response=%v err=%v", response, err)
	}
	if err = store.DeleteMatching(t.Context(), nativePredicate(t, "value == 9007199254740993"), fixtureBudget); err != nil {
		t.Fatal(err)
	}
	response, err = store.QueryByScan(t.Context(), &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("query")}, fixtureBudget)
	if err != nil || len(response.Results) != 0 {
		t.Fatalf("custom deletion response=%v err=%v", response, err)
	}
	for _, definition := range []string{
		"id text, content text, metadata text, embedding vector<float,2>, PRIMARY KEY (id,content)",
		"id text, content text PRIMARY KEY, metadata text, embedding vector<float,2>",
		"id text PRIMARY KEY, content text, metadata text, embedding vector<double,2>",
		"id text PRIMARY KEY, content text, metadata int, embedding vector<float,2>",
	} {
		if err = f.session.Query("CREATE TABLE " + f.table() + " (" + definition + ")").ExecContext(t.Context()); err != nil {
			t.Fatal(err)
		}
		if invalid, schemaErr := NewStore(t.Context(), f.config(SimilarityCosine, false)); invalid != nil || schemaErr == nil {
			t.Fatalf("invalid schema %s: store=%v err=%v", definition, invalid, schemaErr)
		}
		if err = f.session.Query("DROP TABLE " + f.table()).ExecContext(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
}

func TestNativeDeletionRetainsExactCASToken(t *testing.T) {
	f := newNativeFixture(t)
	store := f.store(t, SimilarityCosine)
	nativeIndex(t, store, &document.Document{ID: "same", Text: "query"})
	if applied, err := f.session.Query("UPDATE "+f.table()+" SET metadata=? WHERE id=? IF EXISTS", ` { "value" : "\u0061" } `, "same").Consistency(gocql.Quorum).SerialConsistency(gocql.Serial).DefaultTimestamp(false).MapScanCASContext(t.Context(), map[string]any{}); err != nil || !applied {
		t.Fatalf("native metadata injection: applied=%t, %v", applied, err)
	}
	if err := store.DeleteMatching(t.Context(), nativePredicate(t, "value == 'a'"), fixtureBudget); err != nil {
		t.Fatal(err)
	}
	if len(f.ids(t)) != 0 {
		t.Fatal("raw CAS token was re-encoded")
	}
}

func TestNativeWholeIndexPreflightAndInvalidQuery(t *testing.T) {
	f := newNativeFixture(t)
	store := f.store(t, SimilarityCosine)
	docs := make([]*document.Document, 17)
	for i := range docs {
		docs[i] = &document.Document{ID: fmt.Sprintf("doc-%03d", i), Text: "query"}
	}
	docs[16].Text = "zero"
	if err := store.Upsert(t.Context(), fixtureBatch(docs)); err == nil {
		t.Fatal("accepted invalid vector in later batch")
	}
	if len(f.ids(t)) != 0 {
		t.Fatal("late preflight failure published an earlier batch")
	}
	nativeIndex(t, store, &document.Document{ID: "same", Text: "query"})
	for _, text := range []string{"zero", "overflow", "different width"} {
		response, err := store.QueryByScan(t.Context(), &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector(text)}, fixtureBudget)
		if response != nil || err == nil {
			t.Fatalf("invalid query %s: response=%v err=%v", text, response, err)
		}
	}
}

const fixtureSpace vectorstore.SpaceID = "fixture.v1"

var fixtureBudget = vectorstore.ScanBudget{MaxRecords: 1000, MaxBytes: 16 << 20, Timeout: time.Minute}

func fixtureBatch(docs []*document.Document) *vectorstore.Batch {
	b := &vectorstore.Batch{Space: fixtureSpace}
	for _, d := range docs {
		b.Records = append(b.Records, vectorstore.Record{Document: d, Values: fixtureVector(d.Text)})
	}
	return b
}

func TestNativeMutationProtocolIgnoresClientClockSkew(t *testing.T) {
	f := newNativeFixture(t)
	store := f.store(t, SimilarityCosine)
	f.timestamp = time.Now().Add(5 * time.Minute).UnixMicro()
	nativeIndex(t, store, &document.Document{ID: "one", Text: "query", Metadata: metadata.Map{"tenant": json.RawMessage(`"a"`)}})
	var writeTime int64
	if err := f.session.Query("SELECT writetime(content) FROM "+f.table()+" WHERE id=?", "one").ScanContext(t.Context(), &writeTime); err != nil {
		t.Fatal(err)
	}
	if writeTime >= f.timestamp {
		t.Fatalf("row mutation inherited the caller's future timestamp: %d >= %d", writeTime, f.timestamp)
	}
	nativeIndex(t, store, &document.Document{ID: "one", Text: "far", Metadata: metadata.Map{"tenant": json.RawMessage(`"a"`)}})
	if err := store.DeleteMatching(t.Context(), filter.EQ("tenant", "a"), fixtureBudget); err != nil {
		t.Fatal(err)
	}
	if got := f.ids(t); len(got) != 0 {
		t.Fatalf("acknowledged conditional deletion retained rows: %v", got)
	}
	nativeIndex(t, store, &document.Document{ID: "one", Text: "query"})
	if err := store.DeleteIDs(t.Context(), []string{"one", "one", "missing"}); err != nil {
		t.Fatal(err)
	}
	if got := f.ids(t); len(got) != 0 {
		t.Fatalf("acknowledged ID deletion retained rows: %v", got)
	}
	nativeIndex(t, store, &document.Document{ID: "one", Text: "far"})
	response, err := store.QueryByScan(t.Context(), &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("query")}, fixtureBudget)
	if err != nil || response.First() == nil || response.First().Document.Text != "far" {
		t.Fatalf("deleted identity could not be inserted again: %v, %v", response, err)
	}
}

func TestNativeUpsertReportsDeletionBetweenConditionalWrites(t *testing.T) {
	f := newNativeFixture(t)
	store := f.store(t, SimilarityCosine)
	nativeIndex(t, store, &document.Document{ID: "raced", Text: "query"})
	deleted := false
	f.beforeQuery = func(statement string, values []any) {
		if strings.HasPrefix(statement, "UPDATE ") && values[len(values)-1] == "raced" {
			f.beforeQuery = nil
			if err := store.DeleteIDs(t.Context(), []string{"raced"}); err != nil {
				t.Fatal(err)
			}
			deleted = true
		}
	}
	err := store.Upsert(t.Context(), fixtureBatch([]*document.Document{{ID: "prefix", Text: "query"}, {ID: "raced", Text: "far"}, {ID: "suffix", Text: "query"}}))
	var writeErr *vectorstore.WriteError
	if !errors.As(err, &writeErr) || !deleted {
		t.Fatalf("concurrent deletion had no explicit failed write: %v, deleted=%t", err, deleted)
	}
	outcome := writeErr.Outcome()
	if !slices.Equal(outcome.Acknowledged, []string{"prefix"}) || !slices.Equal(outcome.Rejected, []string{"raced"}) || !slices.Equal(outcome.Unattempted, []string{"suffix"}) || len(outcome.Unconfirmed) != 0 {
		t.Fatalf("conditional evidence was lost: %+v", outcome)
	}
	if got := f.ids(t); !slices.Equal(got, []string{"prefix"}) {
		t.Fatalf("conditional rejection resurrected or attempted a document: %v", got)
	}
}

func TestNativeLegacyMixedWritesCanAcknowledgeAnIneffectiveDelete(t *testing.T) {
	f := newNativeFixture(t)
	f.store(t, SimilarityCosine)
	future := time.Now().Add(5 * time.Minute).UnixMicro()
	// A legacy ordinary write can outrank the timestamp chosen by Paxos even
	// when its conditional delete reports applied. Fresh tables avoid this history.
	if err := f.session.Query("INSERT INTO "+f.table()+" (id,content,metadata,embedding) VALUES (?,?,?,?)", "legacy", "query", `{}`, []float32{1, 0}).WithTimestamp(future).ExecContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	applied, err := f.session.Query("DELETE FROM "+f.table()+" WHERE id=? IF content=? AND metadata=? AND embedding=?", "legacy", "query", `{}`, []float32{1, 0}).Consistency(gocql.Quorum).SerialConsistency(gocql.Serial).DefaultTimestamp(false).MapScanCASContext(t.Context(), map[string]any{})
	if err != nil || !applied {
		t.Fatalf("legacy native CAS response: applied=%t, %v", applied, err)
	}
	var content string
	if err := f.session.Query("SELECT content FROM "+f.table()+" WHERE id=?", "legacy").Consistency(gocql.Quorum).ScanContext(t.Context(), &content); err != nil || content != "query" {
		t.Fatalf("legacy counterexample changed: content=%q, %v", content, err)
	}
}

func TestNativeTableBindingRejectsTheFormerMutationProtocol(t *testing.T) {
	f := newNativeFixture(t)
	store := f.store(t, SimilarityCosine)
	nativeIndex(t, store, &document.Document{ID: "current", Text: "query"})
	reopened, err := NewStore(t.Context(), f.config(SimilarityCosine, false))
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.DeleteIDs(t.Context(), []string{"current"}); err != nil {
		t.Fatal(err)
	}
	legacyBinding := fixtureSpace.StorageKey()
	if err := f.session.Query("ALTER TABLE " + f.table() + " WITH comment = '" + legacyBinding + "'").ExecContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, initialize := range []bool{false, true} {
		if opened, err := NewStore(t.Context(), f.config(SimilarityCosine, initialize)); opened != nil || !errors.Is(err, vectorstore.ErrIncompatibleSpace) {
			t.Fatalf("former ordinary-write binding admitted: create=%t, store=%v, error=%v", initialize, opened, err)
		}
	}
	var binding string
	if err := f.session.Query("SELECT comment FROM system_schema.tables WHERE keyspace_name=? AND table_name=?", f.keyspace, "docs").ScanContext(t.Context(), &binding); err != nil {
		t.Fatal(err)
	}
	if binding != legacyBinding {
		t.Fatal("opening silently upgraded a former mutation protocol")
	}
}
