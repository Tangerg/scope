//go:build integration

package neo4j

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	native "github.com/neo4j/neo4j-go-driver/v5/neo4j"
	nativeconfig "github.com/neo4j/neo4j-go-driver/v5/neo4j/config"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
	"github.com/Tangerg/scope/core/vectorstore/storetest"
)

type nativeFixture struct {
	driver          native.DriverWithContext
	label           string
	model           embedding.Model
	calls           atomic.Int64
	beforeEmbedding func(context.Context) error
}

func newNativeFixture(t *testing.T) *nativeFixture {
	t.Helper()
	dsn, username, password := os.Getenv("SCOPE_NEO4J_DSN"), os.Getenv("SCOPE_NEO4J_USERNAME"), os.Getenv("SCOPE_NEO4J_PASSWORD")
	if dsn == "" || username == "" || password == "" {
		t.Fatal("SCOPE_NEO4J_DSN, SCOPE_NEO4J_USERNAME and SCOPE_NEO4J_PASSWORD are required with -tags=integration; use an isolated Neo4j 5.18+ server")
	}
	driver, err := native.NewDriverWithContext(dsn, native.BasicAuth(username, password, ""), func(cfg *nativeconfig.Config) { cfg.MaxTransactionRetryTime = 2 * time.Second })
	if err != nil {
		t.Fatal(err)
	}
	f := &nativeFixture{driver: driver, label: "ScopeNative_" + rand.Text()}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := driver.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	if err := driver.VerifyConnectivity(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if _, err := f.query(ctx, "MATCH (n:"+quoteIdentifier(f.label)+") DETACH DELETE n", nil); err != nil {
			t.Error(err)
		}
		records, err := f.query(ctx, "SHOW CONSTRAINTS YIELD name, labelsOrTypes WHERE labelsOrTypes = [$label] RETURN name", map[string]any{"label": f.label})
		if err != nil {
			t.Error(err)
			return
		}
		for _, record := range records {
			if _, err := f.query(ctx, "DROP CONSTRAINT "+quoteIdentifier(record.Values[0].(string)), nil); err != nil {
				t.Error(err)
			}
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

func (n *nativeFixture) config(metric SimilarityFunction, initialize bool) StoreConfig {
	return StoreConfig{Driver: n.driver, Label: n.label, EmbeddingModel: n.model, DocumentBatcher: nativeBatcher{}, Similarity: metric, InitializeSchema: initialize}
}
func (n *nativeFixture) store(t *testing.T, metric SimilarityFunction) *Store {
	t.Helper()
	store, err := NewStore(t.Context(), n.config(metric, true))
	if err != nil {
		t.Fatal(err)
	}
	return store
}
func (n *nativeFixture) query(ctx context.Context, query string, params map[string]any) (records []*native.Record, err error) {
	session := n.driver.NewSession(ctx, native.SessionConfig{})
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		err = errors.Join(err, session.Close(closeCtx))
	}()
	result, err := session.Run(ctx, query, params)
	if err != nil {
		return nil, err
	}
	return result.Collect(ctx)
}
func (n *nativeFixture) clear(t *testing.T) {
	t.Helper()
	if _, err := n.query(t.Context(), "MATCH (n:"+quoteIdentifier(n.label)+") DETACH DELETE n", nil); err != nil {
		t.Fatal(err)
	}
}
func (n *nativeFixture) ids(t *testing.T) []string {
	t.Helper()
	records, err := n.query(t.Context(), "MATCH (n:"+quoteIdentifier(n.label)+") RETURN n.id AS id", nil)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, len(records))
	for i, r := range records {
		ids[i] = r.Values[0].(string)
	}
	slices.Sort(ids)
	return ids
}

type nativeBatcher struct{}

func (nativeBatcher) Batch(_ context.Context, documents []*document.Document) ([][]*document.Document, error) {
	var batches [][]*document.Document
	for chunk := range slices.Chunk(documents, 64) {
		batches = append(batches, chunk)
	}
	return batches, nil
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
					response, err := store.Search(ctx, &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{Filter: predicate, TopK: len(docs)}})
					if err != nil {
						return nil, err
					}
					ids := make([]string, len(response.Results))
					for i, result := range response.Results {
						ids[i] = result.Document.ID
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

func TestNativeCanonicalRecordsAndTopK(t *testing.T) {
	for _, metric := range []SimilarityFunction{SimilarityCosine, SimilarityEuclidean} {
		t.Run(metric.String(), func(t *testing.T) {
			f := newNativeFixture(t)
			store := f.store(t, metric)
			rows := []*document.Document{
				{ID: "nil", Text: "query"}, {ID: "empty", Text: "query", Metadata: metadata.Map{}},
				{ID: "raw/路径`", Text: "far", Metadata: metadata.Map{"ok": json.RawMessage(`true`), "null": json.RawMessage(`null`), "nested": json.RawMessage(`{"values":[null,9007199254740993.0,1e1000]}`), "precise": json.RawMessage(`1.00000000000000001`)}},
				{ID: "closer", Text: "query", Metadata: metadata.Map{"ok": json.RawMessage(`false`)}},
			}
			nativeIndex(t, store, rows...)
			response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{TopK: len(rows)}})
			if err != nil {
				t.Fatal(err)
			}
			if len(response.Results) != len(rows) {
				t.Fatal(response)
			}
			for _, result := range response.Results {
				source := rows[slices.IndexFunc(rows, func(d *document.Document) bool { return d.ID == result.Document.ID })]
				if !source.Metadata.Equal(result.Document.Metadata) || (source.Metadata == nil) != (result.Document.Metadata == nil) {
					t.Fatalf("roundtrip id=%s metadata=%s want %s", source.ID, result.Document.Metadata, source.Metadata)
				}
			}
			if response.Results[0].Document.ID != "closer" || response.Results[1].Document.ID != "empty" || response.Results[2].Document.ID != "nil" {
				t.Fatal("equal native scores must use ID order", response)
			}
			response, err = store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{TopK: 1, Filter: nativePredicate(t, "ok == true")}})
			if err != nil || len(response.Results) != 1 || response.Results[0].Document.ID != "raw/路径`" {
				t.Fatalf("filtered TopK response=%v err=%v", response, err)
			}
			score := response.Results[0].Score
			cutoff := vectorstore.Score(math.Nextafter(float64(score), 1))
			response, err = store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{TopK: 1, MinScore: cutoff, Filter: nativePredicate(t, "ok == true")}})
			if err != nil || len(response.Results) != 0 {
				t.Fatalf("threshold response=%v err=%v", response, err)
			}
			// Reindex replaces the whole property projection; no old metadata keys survive.
			nativeIndex(t, store, &document.Document{ID: "raw/路径`", Text: "query", Metadata: metadata.Map{}})
			if err := store.DeleteIDs(t.Context(), []string{"nil", "unknown"}); err != nil {
				t.Fatal(err)
			}
			if err := store.DeleteWhere(t.Context(), nativePredicate(t, "ok is null")); err != nil {
				t.Fatal(err)
			}
			if got := f.ids(t); !slices.Equal(got, []string{"closer"}) {
				t.Fatal(got)
			}
		})
	}
}

func TestNativePreflightFailureHasNoModelOrDeleteEffects(t *testing.T) {
	f := newNativeFixture(t)
	store := f.store(t, SimilarityCosine)
	docs := make([]*document.Document, 1030)
	for i := range docs {
		docs[i] = &document.Document{ID: fmt.Sprintf("doc-%04d", i), Text: "query", Metadata: metadata.Map{"rank": json.RawMessage(`1`)}}
	}
	docs[len(docs)-1].Metadata["rank"] = json.RawMessage(`"wrong type"`)
	nativeIndex(t, store, docs...)
	calls := f.calls.Load()
	predicate := nativePredicate(t, "rank < 2")
	response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{TopK: 1, MinScore: 1, Filter: predicate}})
	if response != nil || err == nil || f.calls.Load() != calls {
		t.Fatalf("response=%v err=%v model calls=%d want %d", response, err, f.calls.Load(), calls)
	}
	if err = store.DeleteWhere(t.Context(), predicate); err == nil {
		t.Fatal("accepted wrong-type predicate")
	}
	if len(f.ids(t)) != len(docs) {
		t.Fatal("preflight deleted records")
	}
	// Valid large batches cross both native score and delete chunk boundaries.
	nativeIndex(t, store, &document.Document{ID: docs[len(docs)-1].ID, Text: "query", Metadata: metadata.Map{"rank": json.RawMessage(`1`)}})
	response, err = store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{TopK: 1030, Filter: predicate}})
	if err != nil || len(response.Results) != 1030 {
		t.Fatalf("response count=%v err=%v", response, err)
	}
	if err := store.DeleteWhere(t.Context(), predicate); err != nil {
		t.Fatal(err)
	}
	if len(f.ids(t)) != 0 {
		t.Fatal("large delete left records")
	}
}

func TestNativeStrictStoredSchema(t *testing.T) {
	f := newNativeFixture(t)
	store := f.store(t, SimilarityCosine)
	for _, mutation := range []string{
		"SET n.`metadata.value` = 'old' REMOVE n.metadata",
		"SET n.foreign = true",
		"SET n.metadata = '{\"value\":1,\"value\":2}'",
		"SET n.embedding = [0.0,0.0]",
		"SET n.embedding = [1,0]",
		"REMOVE n.text",
	} {
		t.Run(mutation, func(t *testing.T) {
			f.clear(t)
			nativeIndex(t, store, &document.Document{ID: "bad", Text: "query"})
			if _, err := f.query(t.Context(), "MATCH (n:"+quoteIdentifier(f.label)+") "+mutation, nil); err != nil {
				t.Fatal(err)
			}
			before := f.calls.Load()
			response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{Filter: nativePredicate(t, "value == 'hidden'"), TopK: 1, MinScore: 1}})
			if response != nil || err == nil || f.calls.Load() != before {
				t.Fatalf("malformed stored node response=%v err=%v calls=%d", response, err, f.calls.Load())
			}
			if err := store.DeleteWhere(t.Context(), nativePredicate(t, "value == 'hidden'")); err == nil {
				t.Fatal("delete accepted malformed stored node")
			}
			if len(f.ids(t)) != 1 {
				t.Fatal("malformed preflight mutated nodes")
			}
		})
	}
}

func TestNativeIndexRollsBackLateConstraintFailure(t *testing.T) {
	f := newNativeFixture(t)
	store := f.store(t, SimilarityCosine)
	if _, err := f.query(t.Context(), "CREATE CONSTRAINT FOR (n:"+quoteIdentifier(f.label)+") REQUIRE n.text IS UNIQUE", nil); err != nil {
		t.Fatal(err)
	}
	docs := make([]*document.Document, 1030)
	for i := range docs {
		docs[i] = &document.Document{ID: fmt.Sprintf("doc-%04d", i), Text: fmt.Sprintf("text-%04d", i)}
	}
	docs[len(docs)-1].Text = docs[0].Text
	if err := store.Index(t.Context(), &vectorstore.IndexRequest{Documents: docs}); err == nil {
		t.Fatal("accepted late uniqueness violation")
	}
	if got := f.ids(t); len(got) != 0 {
		t.Fatalf("partial publication: %d records", len(got))
	}
	// Native metric validation also rejects vectors before publication.
	for _, text := range []string{"zero", "overflow"} {
		if err := store.Index(t.Context(), &vectorstore.IndexRequest{Documents: []*document.Document{{ID: "bad", Text: text}}}); err == nil {
			t.Fatalf("accepted %s vector", text)
		}
		if len(f.ids(t)) != 0 {
			t.Fatal("invalid vector published")
		}
	}
}

func TestNativeSearchUsesCapturedDocumentAndVector(t *testing.T) {
	f := newNativeFixture(t)
	store := f.store(t, SimilarityCosine)
	nativeIndex(t, store, &document.Document{ID: "captured", Text: "query", Metadata: metadata.Map{"value": json.RawMessage(`"before"`)}})
	f.beforeEmbedding = func(ctx context.Context) error {
		_, err := f.query(ctx, "MATCH (n:"+quoteIdentifier(f.label)+") SET n.text = 'far', n.embedding = [0.0,1.0], n.metadata = '{\"value\":\"after\"}'", nil)
		return err
	}
	response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query"})
	if err != nil {
		t.Fatal(err)
	}
	result := response.First()
	if result == nil || result.Score != 1 || result.Document.Text != "query" || string(result.Document.Metadata["value"]) != `"before"` {
		t.Fatalf("captured result=%v", result)
	}
	f.beforeEmbedding = nil
	response, err = store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query"})
	if err != nil || response.First().Score != 0.5 || response.First().Document.Text != "far" {
		t.Fatalf("current result=%v err=%v", response, err)
	}
}

func TestNativeDeletePreflightRetainsNodeLock(t *testing.T) {
	f := newNativeFixture(t)
	store := f.store(t, SimilarityCosine)
	nativeIndex(t, store, &document.Document{ID: "locked", Text: "query"})
	selected := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan error, 1)
	abort := errors.New("abort transaction after preflight")
	go func() {
		_, err := store.transact(t.Context(), native.AccessModeWrite, func(tx native.ManagedTransaction) (any, error) {
			nodes, err := store.selectNodes(t.Context(), tx, nil, true)
			if err != nil {
				return nil, err
			}
			if len(nodes) != 1 {
				return nil, fmt.Errorf("got %d locked nodes", len(nodes))
			}
			close(selected)
			select {
			case <-release:
			case <-t.Context().Done():
				return nil, t.Context().Err()
			}
			return nil, abort
		})
		finished <- err
	}()
	select {
	case <-selected:
	case err := <-finished:
		t.Fatal(err)
	case <-time.After(10 * time.Second):
		t.Fatal("preflight not ready")
	}
	writerCtx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	query := "MATCH (n:" + quoteIdentifier(f.label) + " {id:'locked'}) SET n.text = 'replacement' RETURN n.id"
	writerFinished := make(chan error, 1)
	go func() { _, err := f.query(writerCtx, query, nil); writerFinished <- err }()
	observerCtx, stop := context.WithTimeout(t.Context(), 5*time.Second)
	blocked := false
	var observeErr error
	for !blocked && observerCtx.Err() == nil {
		records, err := f.query(observerCtx, "SHOW TRANSACTIONS YIELD currentQuery, status WHERE currentQuery = $query RETURN status", map[string]any{"query": query})
		if err != nil {
			observeErr = err
			break
		}
		for _, record := range records {
			if strings.HasPrefix(record.Values[0].(string), "Blocked by:") {
				blocked = true
			}
		}
		if !blocked {
			select {
			case <-observerCtx.Done():
			case <-time.After(10 * time.Millisecond):
			}
		}
	}
	stop()
	close(release)
	if err := <-finished; !errors.Is(err, abort) {
		t.Fatal(err)
	}
	if writerErr := <-writerFinished; writerErr != nil || !blocked || observeErr != nil {
		t.Fatalf("native writer blocked=%v observation=%v writer=%v", blocked, observeErr, writerErr)
	}
	records, err := f.query(t.Context(), "MATCH (n:"+quoteIdentifier(f.label)+" {id:'locked'}) RETURN n.text", nil)
	if err != nil || len(records) != 1 || records[0].Values[0] != "replacement" {
		t.Fatalf("records=%v err=%v", records, err)
	}
}

func TestNativeConstructorAndCancellation(t *testing.T) {
	f := newNativeFixture(t)
	if store, err := NewStore(t.Context(), f.config(SimilarityCosine, false)); store != nil || err == nil {
		t.Fatalf("missing constraint store=%v err=%v", store, err)
	}
	store := f.store(t, SimilarityCosine)
	if _, err := NewStore(t.Context(), f.config(SimilarityCosine, false)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	response, err := store.Search(ctx, &vectorstore.SearchRequest{Query: "query"})
	if response != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("response=%v err=%v", response, err)
	}
	if err := store.DeleteIDs(ctx, []string{"missing"}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if store, err := NewStore(ctx, f.config(SimilarityCosine, true)); store != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("store=%v err=%v", store, err)
	}
}

func TestNativeConfiguredPropertiesAndVectorDimensions(t *testing.T) {
	f := newNativeFixture(t)
	config := f.config(SimilarityEuclidean, true)
	config.IDProperty, config.TextProperty, config.MetadataProperty, config.EmbeddingProperty = "key", "content", "facts", "vector"
	store, err := NewStore(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	nativeIndex(t, store, &document.Document{ID: "original", Text: "query", Metadata: metadata.Map{"value": json.RawMessage(`{"nested":true}`)}})
	response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{Filter: nativePredicate(t, "value['nested'] == true")}})
	if err != nil || response.First() == nil || response.First().Document.ID != "original" {
		t.Fatalf("custom properties response=%v err=%v", response, err)
	}
	response, err = store.Search(t.Context(), &vectorstore.SearchRequest{Query: "different width"})
	if response != nil || err == nil {
		t.Fatalf("different dimensions response=%v err=%v", response, err)
	}
	if err = store.DeleteWhere(t.Context(), nativePredicate(t, "value['nested'] == true")); err != nil {
		t.Fatal(err)
	}
	records, err := f.query(t.Context(), "MATCH (n:"+quoteIdentifier(f.label)+") RETURN count(n)", nil)
	if err != nil || len(records) != 1 || records[0].Values[0] != int64(0) {
		t.Fatalf("records=%v err=%v", records, err)
	}
}
