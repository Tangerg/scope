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
	"sync/atomic"
	"testing"
	"time"

	gocql "github.com/apache/cassandra-gocql-driver/v2"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
	"github.com/Tangerg/scope/core/vectorstore/storetest"
)

type nativeFixture struct {
	session         *gocql.Session
	keyspace        string
	model           embedding.Model
	calls           atomic.Int64
	beforeEmbedding func(context.Context) error
	beforeQuery     func(string, []any)
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
	f.model = embedding.ModelFunc(func(ctx context.Context, request *embedding.Request) (*embedding.Response, error) {
		f.calls.Add(1)
		if f.beforeEmbedding != nil {
			if err := f.beforeEmbedding(ctx); err != nil {
				return nil, err
			}
		}
		outputs := make([]*embedding.Output, len(request.Texts))
		for i, text := range request.Texts {
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
			outputs[i] = &embedding.Output{Embedding: vector}
		}
		return embedding.NewResponse(outputs, nil)
	})
	return f
}
func (n *nativeFixture) Query(statement string, values ...any) *gocql.Query {
	if n.beforeQuery != nil {
		n.beforeQuery(statement, values)
	}
	return n.session.Query(statement, values...)
}
func (n *nativeFixture) config(metric SimilarityFunction, create bool) StoreConfig {
	dimensions := 0
	if create {
		dimensions = 2
	}
	return StoreConfig{Session: n, KeyspaceName: n.keyspace, TableName: "docs", EmbeddingModel: n.model, DocumentBatcher: nativeBatcher{}, Similarity: metric, InitializeSchema: create, CreateDimensions: dimensions}
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
	if err := store.Index(t.Context(), &vectorstore.IndexRequest{Documents: docs}); err != nil {
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
				if err := store.Index(ctx, &vectorstore.IndexRequest{Documents: docs}); err != nil {
					return nil, err
				}
				if operation == "search" {
					response, err := store.Search(ctx, &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{TopK: len(docs), Filter: predicate}})
					if err != nil {
						return nil, err
					}
					ids := make([]string, len(response.Results))
					for i, r := range response.Results {
						ids[i] = r.Document.ID
					}
					return ids, nil
				}
				if err := store.DeleteWhere(ctx, predicate); err != nil {
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
			response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{TopK: len(docs)}})
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
			response, err = store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{TopK: 1, Filter: nativePredicate(t, "ok == true")}})
			if err != nil || len(response.Results) != 1 || response.First().Document.ID != "original/路径'" {
				t.Fatalf("filtered response=%v err=%v", response, err)
			}
			if err = store.DeleteWhere(t.Context(), nativePredicate(t, "value == 9007199254740993")); err != nil {
				t.Fatal(err)
			}
			if got := f.ids(t); len(got) != 3 || slices.Contains(got, "original/路径'") {
				t.Fatal(got)
			}
		})
	}
}
func TestNativePreflightBeforeModelThresholdAndDeletion(t *testing.T) {
	f := newNativeFixture(t)
	store := f.store(t, SimilarityCosine)
	docs := make([]*document.Document, 40)
	for i := range docs {
		docs[i] = &document.Document{ID: fmt.Sprintf("doc-%03d", i), Text: "query", Metadata: metadata.Map{"rank": json.RawMessage(`1`)}}
	}
	docs[39].Metadata["rank"] = json.RawMessage(`"wrong type"`)
	nativeIndex(t, store, docs...)
	calls := f.calls.Load()
	predicate := nativePredicate(t, "rank < 2")
	response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{TopK: 1, MinScore: 1, Filter: predicate}})
	if response != nil || err == nil || f.calls.Load() != calls {
		t.Fatalf("response=%v err=%v calls=%d want %d", response, err, f.calls.Load(), calls)
	}
	if err = store.DeleteWhere(t.Context(), predicate); err == nil {
		t.Fatal("accepted wrong-type predicate")
	}
	if len(f.ids(t)) != 40 {
		t.Fatal("preflight deleted rows")
	}
	nativeIndex(t, store, &document.Document{ID: "doc-039", Text: "query", Metadata: metadata.Map{"rank": json.RawMessage(`1`)}})
	if err = store.DeleteWhere(t.Context(), predicate); err != nil {
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
			if err := f.session.Query("UPDATE "+f.table()+" SET content=?,metadata=?,embedding=? WHERE id=?", "replacement", `{"value":"after"}`, []float32{0, 1}, "same").ExecContext(t.Context()); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := store.DeleteWhere(t.Context(), nativePredicate(t, "value == 'before'")); err == nil || !strings.Contains(err.Error(), "changed during filter deletion") || !changed {
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
	f.beforeEmbedding = func(ctx context.Context) error {
		return f.session.Query("UPDATE "+f.table()+" SET content=?,metadata=?,embedding=? WHERE id=?", "far", `{"value":"after"}`, []float32{0, 1}, "same").ExecContext(ctx)
	}
	response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query"})
	if err != nil {
		t.Fatal(err)
	}
	if response.First() == nil || response.First().Score != 1 || response.First().Document.Text != "query" || string(response.First().Document.Metadata["value"]) != `"before"` {
		t.Fatal(response)
	}
	f.beforeEmbedding = nil
	response, err = store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query"})
	if err != nil || response.First().Score != .5 || response.First().Document.Text != "far" {
		t.Fatalf("current response=%v err=%v", response, err)
	}
}
func TestNativeDotRetainsRawRanking(t *testing.T) {
	f := newNativeFixture(t)
	store := f.store(t, SimilarityDotProduct)
	nativeIndex(t, store, &document.Document{ID: "a-lower", Text: "query"}, &document.Document{ID: "z-higher", Text: "big"})
	response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "big query", Options: vectorstore.SearchOptions{TopK: 1}})
	if err != nil || response.First() == nil || response.First().Document.ID != "z-higher" || response.First().Score != 1 {
		t.Fatalf("saturated response=%v err=%v", response, err)
	}
	nativeIndex(t, store, &document.Document{ID: "huge", Text: "huge"})
	response, err = store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{TopK: 1}})
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
		if err := f.session.Query("UPDATE "+f.table()+" SET metadata=? WHERE id=?", raw, "bad").ExecContext(t.Context()); err != nil {
			t.Fatal(err)
		}
		calls := f.calls.Load()
		response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{Filter: nativePredicate(t, "a == 'hidden'"), MinScore: 1}})
		if response != nil || err == nil || f.calls.Load() != calls {
			t.Fatalf("malformed response=%v err=%v", response, err)
		}
		if err = store.DeleteWhere(t.Context(), nativePredicate(t, "a == 'hidden'")); err == nil {
			t.Fatal("accepted malformed record")
		}
		if len(f.ids(t)) != 1 {
			t.Fatal("malformed preflight deleted row")
		}
	}
	f.clear(t)
	for _, text := range []string{"zero", "overflow", "different width"} {
		if err := store.Index(t.Context(), &vectorstore.IndexRequest{Documents: []*document.Document{{ID: "bad", Text: text}}}); err == nil {
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
	response, err := store.Search(ctx, &vectorstore.SearchRequest{Query: "query"})
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
	response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{Filter: nativePredicate(t, "value == 9007199254740993")}})
	if err != nil || response.First() == nil || response.First().Document.ID != "original/路径" {
		t.Fatalf("custom response=%v err=%v", response, err)
	}
	if err = store.DeleteWhere(t.Context(), nativePredicate(t, "value == 9007199254740993")); err != nil {
		t.Fatal(err)
	}
	response, err = store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query"})
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
	if err := f.session.Query("UPDATE "+f.table()+" SET metadata=? WHERE id=?", ` { "value" : "\u0061" } `, "same").ExecContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteWhere(t.Context(), nativePredicate(t, "value == 'a'")); err != nil {
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
	if err := store.Index(t.Context(), &vectorstore.IndexRequest{Documents: docs}); err == nil {
		t.Fatal("accepted invalid vector in later batch")
	}
	if len(f.ids(t)) != 0 {
		t.Fatal("late preflight failure published an earlier batch")
	}
	nativeIndex(t, store, &document.Document{ID: "same", Text: "query"})
	for _, text := range []string{"zero", "overflow", "different width"} {
		response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: text})
		if response != nil || err == nil {
			t.Fatalf("invalid query %s: response=%v err=%v", text, response, err)
		}
	}
}
