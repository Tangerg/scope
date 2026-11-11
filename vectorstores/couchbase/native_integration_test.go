//go:build integration

package couchbase_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"math"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/couchbase/gocb/v2"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
	"github.com/Tangerg/scope/core/vectorstore/storetest"
	storecouchbase "github.com/Tangerg/scope/vectorstores/couchbase"
)

type nativeFixture struct {
	cluster    *gocb.Cluster
	bucket     *gocb.Bucket
	scope      string
	collection *gocb.Collection
	tracer     *removalTracer
}

func newNativeFixture(t *testing.T) *nativeFixture {
	t.Helper()
	dsn, username, password, bucketName := os.Getenv("SCOPE_COUCHBASE_DSN"), os.Getenv("SCOPE_COUCHBASE_USERNAME"), os.Getenv("SCOPE_COUCHBASE_PASSWORD"), os.Getenv("SCOPE_COUCHBASE_BUCKET")
	if dsn == "" || username == "" || password == "" || bucketName == "" {
		t.Fatal("SCOPE_COUCHBASE_DSN, SCOPE_COUCHBASE_USERNAME, SCOPE_COUCHBASE_PASSWORD and SCOPE_COUCHBASE_BUCKET are required with -tags=integration; use an isolated Couchbase 8+ server")
	}
	tracer := new(removalTracer)
	cluster, err := gocb.Connect(dsn, gocb.ClusterOptions{Authenticator: gocb.PasswordAuthenticator{Username: username, Password: password}, Tracer: tracer, TimeoutsConfig: gocb.TimeoutsConfig{QueryTimeout: 30 * time.Second, KVTimeout: 10 * time.Second}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := cluster.Close(nil); err != nil {
			t.Error(err)
		}
	})
	bucket := cluster.Bucket(bucketName)
	if err := bucket.WaitUntilReady(20*time.Second, &gocb.WaitUntilReadyOptions{Context: t.Context()}); err != nil {
		t.Fatal(err)
	}
	f := &nativeFixture{cluster: cluster, bucket: bucket, scope: "scope_native_" + strings.ToLower(rand.Text()), tracer: tracer}
	if err := bucket.CollectionsV2().CreateScope(f.scope, &gocb.CreateScopeOptions{Context: t.Context()}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 30*time.Second)
		defer cancel()
		if err := bucket.CollectionsV2().DropScope(f.scope, &gocb.DropScopeOptions{Context: ctx}); err != nil {
			t.Error(err)
		}
	})
	if err := bucket.CollectionsV2().CreateCollection(f.scope, fixtureCollection(), nil, &gocb.CreateCollectionOptions{Context: t.Context()}); err != nil {
		t.Fatal(err)
	}
	f.collection = bucket.Scope(f.scope).Collection(fixtureCollection())
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	for {
		count := 0
		statement := fmt.Sprintf("SELECT RAW COUNT(*) FROM system:keyspaces WHERE `bucket` = %q AND `scope` = %q AND name = %q", bucketName, f.scope, fixtureCollection())
		if err := f.query(ctx, statement, func(raw json.RawMessage) error { return jsonv2.Unmarshal(raw, &count) }); err != nil {
			t.Fatal(err)
		}
		if count == 1 {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("created collection did not become visible to Query Service")
		case <-time.After(100 * time.Millisecond):
		}
	}

	return f
}

func (n *nativeFixture) config(metric storecouchbase.Similarity, initialize bool) storecouchbase.StoreConfig {
	return storecouchbase.StoreConfig{Cluster: n.cluster, BucketName: n.bucket.Name(), ScopeName: n.scope, Space: fixtureSpace, Dimensions: 2, Similarity: metric, InitializeSchema: initialize}
}

func (n *nativeFixture) newStore(t *testing.T) *storecouchbase.Store {
	t.Helper()
	s, err := storecouchbase.NewStore(t.Context(), n.config(storecouchbase.SimilarityCosine, true))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func (n *nativeFixture) query(ctx context.Context, statement string, consume func(json.RawMessage) error) (err error) {
	result, err := n.bucket.Scope(n.scope).Query(statement, &gocb.QueryOptions{Context: ctx, ScanConsistency: gocb.QueryScanConsistencyRequestPlus, UseReplica: gocb.QueryUseReplicaLevelOff})
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, result.Close()) }()
	for result.Next() {
		var raw json.RawMessage
		if err := result.Row(&raw); err != nil {
			return err
		}
		if consume != nil {
			if err := consume(raw); err != nil {
				return err
			}
		}
	}
	return result.Err()
}

func (n *nativeFixture) install(ctx context.Context, s *storecouchbase.Store, docs []*document.Document) error {
	if err := n.query(ctx, "DELETE FROM `"+fixtureCollection()+"`", nil); err != nil {
		return err
	}
	return s.Upsert(ctx, fixtureBatch(docs))
}

type nativeBatcher struct{}

func (n nativeBatcher) Batch(ctx context.Context, docs []*document.Document) ([][]*document.Document, error) {
	return [][]*document.Document{docs}, ctx.Err()
}

func TestNativeDistancesAndFilteredTopK(t *testing.T) {
	for _, metric := range []storecouchbase.Similarity{storecouchbase.SimilarityCosine, storecouchbase.SimilarityL2Norm, storecouchbase.SimilarityDotProduct} {
		t.Run(metric.String(), func(t *testing.T) {
			f := newNativeFixture(t)
			s, err := storecouchbase.NewStore(t.Context(), f.config(metric, true))
			if err != nil {
				t.Fatal(err)
			}
			s, err = storecouchbase.NewStore(t.Context(), f.config(metric, false))
			if err != nil {
				t.Fatal(err)
			}
			docs := []*document.Document{{ID: "near", Text: "near", Metadata: metadata.Map{"active": json.RawMessage(`false`)}}, {ID: "far", Text: "far", Metadata: metadata.Map{"active": json.RawMessage(`true`)}}}
			if err := s.Upsert(t.Context(), fixtureBatch(docs)); err != nil {
				t.Fatal(err)
			}
			response, err := s.QueryByScan(t.Context(), &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("near"), Options: vectorstore.SearchOptions{TopK: 2}}, fixtureBudget)
			if err != nil {
				t.Fatal(err)
			}
			if len(response.Results) != 2 || response.Results[0].Document.ID != "near" || response.Results[1].Document.ID != "far" {
				t.Fatalf("native ranking: %+v", response)
			}
			wantNear, wantFar := 1., .5
			if metric == storecouchbase.SimilarityL2Norm {
				wantFar = 1 / (1 + math.Sqrt(2))
			}
			if metric == storecouchbase.SimilarityDotProduct {
				wantNear = 1 / (1 + math.Exp(-1))
			}
			if math.Abs(response.Results[0].Score.Float64()-wantNear) > 1e-6 || math.Abs(response.Results[1].Score.Float64()-wantFar) > 1e-6 {
				t.Fatal("distance scores differ")
			}
			response, err = s.QueryByScan(t.Context(), &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("near"), Options: vectorstore.SearchOptions{TopK: 1, Filter: filter.EQ("active", true)}}, fixtureBudget)
			if err != nil || len(response.Results) != 1 || response.Results[0].Document.ID != "far" {
				t.Fatalf("TopK excluded lower-ranked matching record: %+v, %v", response, err)
			}
			response, err = s.QueryByScan(t.Context(), &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("near"), Options: vectorstore.SearchOptions{TopK: 2, MinScore: vectorstore.ScoreFromValue((wantNear + wantFar) / 2)}}, fixtureBudget)
			if err != nil || len(response.Results) != 1 || response.Results[0].Document.ID != "near" {
				t.Fatalf("MinScore not applied: %+v, %v", response, err)
			}
			if err := s.DeleteMatching(t.Context(), filter.EQ("active", true), fixtureBudget); err != nil {
				t.Fatal(err)
			}
			if err := s.DeleteIDs(t.Context(), []string{"near", "unknown"}); err != nil {
				t.Fatal(err)
			}
			response, err = s.QueryByScan(t.Context(), &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("near")}, fixtureBudget)
			if err != nil || len(response.Results) != 0 {
				t.Fatalf("native deletion incomplete: %+v, %v", response, err)
			}
		})
	}
}

func TestNativeCoreFilterConformance(t *testing.T) {
	f := newNativeFixture(t)
	s := f.newStore(t)
	storetest.FilterConformance(t, storetest.FilterConfig{Query: func(ctx context.Context, docs []*document.Document, predicate filter.Predicate) ([]string, error) {
		if err := f.install(ctx, s, docs); err != nil {
			return nil, err
		}
		response, err := s.QueryByScan(ctx, &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("query"), Options: vectorstore.SearchOptions{TopK: len(docs), Filter: predicate}}, fixtureBudget)
		if err != nil {
			return nil, err
		}
		var ids []string
		for _, r := range response.Results {
			ids = append(ids, r.Document.ID)
		}
		return ids, nil
	}})
}

func TestNativeCoreDeleteConformance(t *testing.T) {
	f := newNativeFixture(t)
	s := f.newStore(t)
	storetest.FilterConformance(t, storetest.FilterConfig{Query: func(ctx context.Context, docs []*document.Document, predicate filter.Predicate) ([]string, error) {
		if err := f.install(ctx, s, docs); err != nil {
			return nil, err
		}
		if err := s.DeleteMatching(ctx, predicate, fixtureBudget); err != nil {
			return nil, err
		}
		response, err := s.QueryByScan(ctx, &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("query"), Options: vectorstore.SearchOptions{TopK: len(docs)}}, fixtureBudget)
		if err != nil {
			return nil, err
		}
		remaining := map[string]bool{}
		for _, r := range response.Results {
			remaining[r.Document.ID] = true
		}
		var deleted []string
		for _, d := range docs {
			if !remaining[d.ID] {
				deleted = append(deleted, d.ID)
			}
		}
		return deleted, nil
	}})
}

func TestNativeEncodedFactsAndKVIdentity(t *testing.T) {
	f := newNativeFixture(t)
	s := f.newStore(t)
	ids := []string{"ID", "id", "id\x00", "id ", "中文", strings.Repeat("x", 246)}
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
	if err := s.Upsert(t.Context(), fixtureBatch(docs)); err != nil {
		t.Fatal(err)
	}
	response, err := s.QueryByScan(t.Context(), &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("query"), Options: vectorstore.SearchOptions{TopK: len(docs)}}, fixtureBudget)
	if err != nil {
		t.Fatal(err)
	}
	ordered := slices.Clone(ids)
	slices.Sort(ordered)
	if len(response.Results) != len(docs) {
		t.Fatal("KV identities collapsed")
	}
	for i, r := range response.Results {
		if r.Document.ID != ordered[i] {
			t.Fatal("exact tie order changed")
		}
		source := facts
		if r.Document.ID == ids[0] {
			source = nil
		}
		if r.Document.ID == ids[1] {
			source = metadata.Map{}
		}
		encoded, err := source.MarshalJSON()
		if err != nil {
			t.Fatal(err)
		}
		var projected metadata.Map
		if err := projected.UnmarshalJSON(encoded); err != nil {
			t.Fatal(err)
		}
		if !projected.Equal(r.Document.Metadata) || (source == nil) != (r.Document.Metadata == nil) {
			t.Fatal("Core encoded metadata changed")
		}
		value, err := f.collection.Get(r.Document.ID, &gocb.GetOptions{Context: t.Context()})
		if err != nil {
			t.Fatal(err)
		}
		var body map[string]json.RawMessage
		if err := value.Content(&body); err != nil {
			t.Fatal(err)
		}
		if len(body) != 3 || body["id"] != nil {
			t.Fatal("KV identity has a body owner")
		}
		var stored string
		if err := jsonv2.Unmarshal(body["metadata"], &stored); err != nil || stored != string(encoded) {
			t.Fatal("native storage changed Core bytes")
		}
	}
	for _, src := range []string{`huge > 1`, `tiny > 0`, `nested['items'][0] > 1`, `escaped == 'Alice'`} {
		p, err := filter.Parse(src)
		if err != nil {
			t.Fatal(err)
		}
		r, err := s.QueryByScan(t.Context(), &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("query"), Options: vectorstore.SearchOptions{TopK: len(docs), Filter: p}}, fixtureBudget)
		if err != nil || len(r.Results) != len(docs)-2 {
			t.Fatalf("%s: %+v, %v", src, r, err)
		}
	}
	if err := s.DeleteIDs(t.Context(), []string{ids[2], ids[5]}); err != nil {
		t.Fatal(err)
	}
	response, err = s.QueryByScan(t.Context(), &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("query"), Options: vectorstore.SearchOptions{TopK: len(docs)}}, fixtureBudget)
	if err != nil || len(response.Results) != len(docs)-2 {
		t.Fatalf("exact deletion: %+v, %v", response, err)
	}
}

func TestNativeFilterFailurePrecedesEffects(t *testing.T) {
	f := newNativeFixture(t)
	s := f.newStore(t)
	docs := make([]*document.Document, 1030)
	for i := range docs {
		docs[i] = &document.Document{ID: fmt.Sprintf("%08d", i), Text: "text", Metadata: metadata.Map{"n": json.RawMessage(`7`)}}
	}
	docs[len(docs)-1].Metadata = metadata.Map{"n": json.RawMessage(`"wrong"`)}
	if err := s.Upsert(t.Context(), fixtureBatch(docs)); err != nil {
		t.Fatal(err)
	}
	response, err := s.QueryByScan(t.Context(), &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("query"), Options: vectorstore.SearchOptions{TopK: 1, MinScore: 1, Filter: filter.GT("n", 1)}}, fixtureBudget)
	if err == nil || response != nil {
		t.Fatal("wrong-type filter became limited success")
	}
	if err := s.DeleteMatching(t.Context(), filter.GT("n", 1), fixtureBudget); err == nil {
		t.Fatal("wrong-type filter became deletion")
	}

	for _, doc := range docs {
		if _, err := f.collection.Get(doc.ID, &gocb.GetOptions{Context: t.Context()}); err != nil {
			t.Fatalf("preflight changed native key %s: %v", doc.ID, err)
		}
	}
	actualRows := 0
	if err := f.query(t.Context(), "SELECT RAW META(c).id FROM `"+fixtureCollection()+"` c", func(json.RawMessage) error { actualRows++; return nil }); err != nil {
		t.Fatal(err)
	}
	if actualRows != len(docs) {
		t.Fatalf("preflight published partial deletion: %d", actualRows)
	}
}

func TestNativeCapturedRecordRemainsStableAcrossReplacement(t *testing.T) {
	f := newNativeFixture(t)
	s := f.newStore(t)
	if err := s.Upsert(t.Context(), fixtureBatch([]*document.Document{{ID: "target", Text: "before", Metadata: metadata.Map{"active": json.RawMessage(`true`)}}})); err != nil {
		t.Fatal(err)
	}
	response, err := s.QueryByScan(t.Context(), &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("query"), Options: vectorstore.SearchOptions{Filter: filter.EQ("active", true)}}, fixtureBudget)
	if err != nil || len(response.Results) != 1 || response.Results[0].Document.Text != "before" || response.Results[0].Score != 1 {
		t.Fatalf("record facts advanced independently: %+v, %v", response, err)
	}
	if err = s.Upsert(t.Context(), fixtureBatch([]*document.Document{{ID: "target", Text: "far", Metadata: metadata.Map{"active": json.RawMessage(`false`)}}})); err != nil {
		t.Fatal(err)
	}
	response, err = s.QueryByScan(t.Context(), &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("query")}, fixtureBudget)
	if err != nil || len(response.Results) != 1 || response.Results[0].Document.Text != "far" || response.Results[0].Score != .5 {
		t.Fatalf("replacement not published: %+v, %v", response, err)
	}
}

func TestNativeStrictRecordsAndCancellation(t *testing.T) {
	f := newNativeFixture(t)
	s := f.newStore(t)
	if _, err := f.collection.Upsert("legacy", map[string]any{"id": "forged", "content": "text", "metadata": map[string]any{}, "embedding": []float64{1, 0}}, &gocb.UpsertOptions{Context: t.Context()}); err != nil {
		t.Fatal(err)
	}
	if response, err := s.QueryByScan(t.Context(), &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("query")}, fixtureBudget); err == nil || response != nil {
		t.Fatal("obsolete record accepted")
	}
	if err := s.DeleteMatching(t.Context(), filter.EQ("active", true), fixtureBudget); err == nil {
		t.Fatal("obsolete record silently excluded")
	}
	if _, err := f.collection.Get("legacy", &gocb.GetOptions{Context: t.Context()}); err != nil {
		t.Fatal("obsolete record changed on failed preflight")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if response, err := s.QueryByScan(ctx, &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("query")}, fixtureBudget); response != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("search cancellation: %v", err)
	}
	if err := s.DeleteMatching(ctx, filter.EQ("active", true), fixtureBudget); !errors.Is(err, context.Canceled) {
		t.Fatalf("delete cancellation: %v", err)
	}
}

// The SDK invokes the tracer before dispatching remove. The fixture changes the
// real KV record at that boundary; the production CAS must reject its old version.
type removalTracer struct {
	gocb.NoopTracer
	mutex        sync.Mutex
	beforeRemove func()
}

func (r *removalTracer) RequestSpan(parent gocb.RequestSpanContext, name string) gocb.RequestSpan {
	if name == "remove" {
		r.mutex.Lock()
		before := r.beforeRemove
		r.beforeRemove = nil
		r.mutex.Unlock()
		if before != nil {
			before()
		}
	}
	return r.NoopTracer.RequestSpan(parent, name)
}

func (r *removalTracer) arm(before func()) {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	r.beforeRemove = before
}

func TestNativeCASProtectsConcurrentReplacement(t *testing.T) {
	f := newNativeFixture(t)
	s := f.newStore(t)
	if err := s.Upsert(t.Context(), fixtureBatch([]*document.Document{{ID: "target", Text: "before", Metadata: metadata.Map{"active": json.RawMessage(`true`)}}})); err != nil {
		t.Fatal(err)
	}
	replaced := false
	f.tracer.arm(func() {
		replaced = true
		if err := s.Upsert(t.Context(), fixtureBatch([]*document.Document{{ID: "target", Text: "after", Metadata: metadata.Map{"active": json.RawMessage(`false`)}}})); err != nil {
			t.Error(err)
		}
	})
	if err := s.DeleteMatching(t.Context(), filter.EQ("active", true), fixtureBudget); !errors.Is(err, gocb.ErrCasMismatch) {
		t.Fatalf("concurrent replacement was not rejected: %v", err)
	}
	response, err := s.QueryByScan(t.Context(), &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("query")}, fixtureBudget)
	if !replaced || err != nil || len(response.Results) != 1 || response.Results[0].Document.Text != "after" {
		t.Fatalf("CAS deleted unevaluated facts: %+v, %v", response, err)
	}
}

func TestNativeRankingSpansAllBatches(t *testing.T) {
	f := newNativeFixture(t)
	s := f.newStore(t)
	docs := make([]*document.Document, 1030)
	for i := range docs {
		docs[i] = &document.Document{ID: fmt.Sprintf("%08d", i), Text: "far"}
	}
	docs[len(docs)-1].Text = "near"
	if err := s.Upsert(t.Context(), fixtureBatch(docs)); err != nil {
		t.Fatal(err)
	}
	for _, limit := range []int{1, len(docs)} {
		response, err := s.QueryByScan(t.Context(), &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("near"), Options: vectorstore.SearchOptions{TopK: limit}}, fixtureBudget)
		if err != nil || len(response.Results) != limit || response.Results[0].Document.ID != docs[len(docs)-1].ID || response.Results[0].Score != 1 {
			t.Fatalf("ranking across native batches: %+v, %v", response, err)
		}
	}
}

func TestNativeSpaceBoundCollectionKeyBoundary(t *testing.T) {
	f := newNativeFixture(t)
	config := f.config(storecouchbase.SimilarityCosine, true)

	s, err := storecouchbase.NewStore(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	prefix := "scope_native_" + rand.Text()
	id := prefix + strings.Repeat("x", 246-len(prefix))
	col := f.collection
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 10*time.Second)
		defer cancel()
		if _, err := col.Remove(id, &gocb.RemoveOptions{Context: ctx}); err != nil && !errors.Is(err, gocb.ErrDocumentNotFound) {
			t.Error(err)
		}
	})
	if err := s.Upsert(t.Context(), fixtureBatch([]*document.Document{{ID: id, Text: "text"}})); err != nil {
		t.Fatal(err)
	}
	if _, err := col.Get(id, &gocb.GetOptions{Context: t.Context()}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteIDs(t.Context(), []string{id}); err != nil {
		t.Fatal(err)
	}
	if _, err := col.Get(id, &gocb.GetOptions{Context: t.Context()}); !errors.Is(err, gocb.ErrDocumentNotFound) {
		t.Fatal("default key was not deleted exactly")
	}
}

func TestNativeScoreSaturationPreservesDistanceRanking(t *testing.T) {
	f := newNativeFixture(t)

	s, err := storecouchbase.NewStore(t.Context(), f.config(storecouchbase.SimilarityDotProduct, true))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Upsert(t.Context(), &vectorstore.Batch{Space: fixtureSpace, Records: []vectorstore.Record{{Document: &document.Document{ID: "z_near", Text: "near"}, Values: []float64{1000, 0}}, {Document: &document.Document{ID: "a_far", Text: "far"}, Values: []float64{100, 0}}}}); err != nil {
		t.Fatal(err)
	}
	response, err := s.QueryByScan(t.Context(), &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("query"), Options: vectorstore.SearchOptions{TopK: 1}}, fixtureBudget)
	if err != nil || len(response.Results) != 1 || response.Results[0].Document.ID != "z_near" {
		t.Fatalf("rounded Score reversed native distances: %+v, %v", response, err)
	}
}

const fixtureSpace vectorstore.SpaceID = "fixture.v1"

var fixtureBudget = vectorstore.ScanBudget{MaxRecords: 5000, MaxBytes: 64 << 20, Timeout: time.Minute}

func fixtureCollection() string {
	return (storecouchbase.StoreConfig{Space: fixtureSpace, Dimensions: 2}).CollectionName()
}

func fixtureVector(text string) []float64 {
	if text == "far" {
		return []float64{0, 1}
	}
	return []float64{1, 0}
}

func fixtureBatch(docs []*document.Document) *vectorstore.Batch {
	b := &vectorstore.Batch{Space: fixtureSpace}
	for _, d := range docs {
		b.Records = append(b.Records, vectorstore.Record{Document: d, Values: fixtureVector(d.Text)})
	}
	return b
}
