//go:build integration

package redis

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/Tangerg/scope/core/document"

	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

func liveStore(t *testing.T, metric string) (*Store, *goredis.Client, string) {
	t.Helper()
	address := os.Getenv("SCOPE_REDIS_ADDR")
	if address == "" {
		t.Fatal("SCOPE_REDIS_ADDR is required with -tags=integration")
	}
	client := goredis.NewClient(&goredis.Options{Addr: address, Protocol: 3, Username: os.Getenv("SCOPE_REDIS_USERNAME"), Password: os.Getenv("SCOPE_REDIS_PASSWORD"), ContextTimeoutEnabled: true, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second})
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Error(err)
		}
	})
	name := "scope-redis-" + rand.Text()
	prefix := name + "*?[\\]:" + fixtureSpace.StorageKey() + ":"
	if err := client.FTCreate(t.Context(), name, &goredis.FTCreateOptions{OnHash: true, Prefix: []any{prefix}}, &goredis.FieldSchema{FieldName: embeddingField, FieldType: goredis.SearchFieldTypeVector, VectorArgs: &goredis.FTVectorArgs{FlatOptions: &goredis.FTFlatOptions{Type: "FLOAT32", Dim: 2, DistanceMetric: metric}}}, &goredis.FieldSchema{FieldName: termsField, FieldType: goredis.SearchFieldTypeTag}).Err(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 10*time.Second)
		defer cancel()
		if err := client.Do(ctx, "FT.DROPINDEX", name, "DD").Err(); err != nil {
			t.Errorf("cleanup index: %v", err)
		}
	})
	store, err := NewStore(t.Context(), StoreConfig{Client: client, ScanNodes: func(ctx context.Context, visit func(context.Context, *goredis.Client) error) error {
		return visit(ctx, client)
	}, IndexName: name, Space: fixtureSpace})
	if err != nil {
		t.Fatal(err)
	}
	return store, client, prefix
}

func TestLiveMetadataAndNativeSchemaRoundTrip(t *testing.T) {
	store, client, prefix := liveStore(t, "COSINE")
	facts := []metadata.Map{nil, {}, {"content": json.RawMessage(`"value"`), "embedding": json.RawMessage(`{"nested":[1,null]}`), "metadata_json": json.RawMessage(`9007199254740993`), "decimal": json.RawMessage(`1.0000000000000000001`), "huge": json.RawMessage(`1e1000`)}}
	ids := []string{" spaced ", "*?[]\\", "中文🙂"}
	docs := make([]*document.Document, len(facts))
	for i := range facts {
		docs[i] = &document.Document{ID: ids[i], Text: "text", Metadata: facts[i]}
	}
	if err := store.Upsert(t.Context(), fixtureBatch(docs)); err != nil {
		t.Fatal(err)
	}
	if err := awaitVisibleDocuments(t.Context(), store, docs); err != nil {
		t.Fatal(err)
	}
	response, err := fixtureQuery(t.Context(), store, &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("query"), Options: vectorstore.SearchOptions{TopK: len(docs)}})
	if err != nil {
		t.Fatal(err)
	}
	for _, doc := range docs {
		found := slices.IndexFunc(response.Results, func(hit *vectorstore.SearchResult) bool { return hit.Document.ID == doc.ID })
		if found < 0 || !doc.Metadata.Equal(response.Results[found].Document.Metadata) || (doc.Metadata == nil) != (response.Results[found].Document.Metadata == nil) {
			t.Fatalf("native round trip changed %q", doc.ID)
		}
		fields, readErr := client.HGetAll(t.Context(), prefix+doc.ID).Result()
		if readErr != nil || len(fields) != 4 {
			t.Fatalf("native HASH owns duplicate fields: %v, %v", slices.Sorted(maps.Keys(fields)), readErr)
		}
	}
	if err = client.HSet(t.Context(), prefix+docs[0].ID, "shadow", "value").Err(); err != nil {
		t.Fatal(err)
	}
	if response, err = fixtureQuery(t.Context(), store, &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("query"), Options: vectorstore.SearchOptions{Filter: filter.IsNull("absent")}}); err == nil || response != nil {
		t.Fatal("shadow record became successful search")
	}
	if err = store.Upsert(t.Context(), fixtureBatch(docs[:1])); err != nil {
		t.Fatal(err)
	}
	if count, readErr := client.HLen(t.Context(), prefix+docs[0].ID).Result(); readErr != nil || count != 4 {
		t.Fatalf("whole HASH replacement retained shadow fields: %d, %v", count, readErr)
	}
	if err = store.DeleteIDs(t.Context(), append(ids, ids[0], "unknown")); err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		if count, err := client.Exists(t.Context(), prefix+id).Result(); err != nil || count != 0 {
			t.Fatalf("explicit deletion retained %q: %d, %v", id, count, err)
		}
	}
}

func TestLiveNativeMetricsAndFilteredGroups(t *testing.T) {
	for _, metric := range []string{"COSINE", "L2", "IP"} {
		t.Run(metric, func(t *testing.T) {
			store, client, _ := liveStore(t, metric)
			docs := make([]*document.Document, 300)
			for i := range docs {
				docs[i] = &document.Document{ID: fmt.Sprintf("%04d", i), Text: "near"}
			}
			docs[0].Text = "far"
			if err := store.Upsert(t.Context(), fixtureBatch(docs)); err != nil {
				t.Fatal(err)
			}
			if err := awaitVisibleDocuments(t.Context(), store, docs); err != nil {
				t.Fatal(err)
			}
			response, err := fixtureQuery(t.Context(), store, &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("near"), Options: vectorstore.SearchOptions{TopK: 300, Filter: filter.IsNull("unused")}})
			if err != nil {
				t.Fatal(err)
			}
			if len(response.Results) != len(docs) || response.Results[len(docs)-1].Document.ID != "0000" {
				t.Fatalf("native rank or group membership changed: %d hits, %v", len(response.Results), err)
			}
			native, err := client.FTSearchWithArgs(t.Context(), store.indexName, "*=>[KNN 300 @embedding $vec AS __vector_distance]", &goredis.FTSearchOptions{Params: map[string]any{"vec": float32sToBytes([]float32{1, 0})}, Return: []goredis.FTSearchReturn{{FieldName: distanceFieldName}}, Limit: 300, SortBy: []goredis.FTSearchSortBy{{FieldName: distanceFieldName, Asc: true}}, DialectVersion: 2}).Result()
			if err != nil || len(native.Docs) != len(response.Results) {
				t.Fatalf("independent native rank: %v", err)
			}
			wantedDistance := "1"
			if metric == "L2" {
				wantedDistance = "2"
			}
			if native.Docs[len(native.Docs)-1].Fields[distanceFieldName] != wantedDistance {
				t.Fatalf("native distance %s: %v", metric, native.Docs[len(native.Docs)-1].Fields)
			}
			if err = store.DeleteMatching(t.Context(), filter.IsNull("unused"), fixtureBudget()); err != nil {
				t.Fatal(err)
			}
			if response, err = fixtureQuery(t.Context(), store, &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("near")}); err != nil || len(response.Results) != 0 {
				t.Fatalf("conditional deletion retained documents: %#v, %v", response, err)
			}
		})
	}
}

type staleDeleteHook struct {
	client *goredis.Client
	key    string
	once   atomic.Bool
}

func (s *staleDeleteHook) DialHook(next goredis.DialHook) goredis.DialHook { return next }

func (s *staleDeleteHook) ProcessPipelineHook(next goredis.ProcessPipelineHook) goredis.ProcessPipelineHook {
	return next
}

func (s *staleDeleteHook) ProcessHook(next goredis.ProcessHook) goredis.ProcessHook {
	return func(ctx context.Context, command goredis.Cmder) error {
		if command.Name() == "eval" && command.Args()[1] == deleteObservedMetadata && s.once.CompareAndSwap(false, true) {
			tags, err := tagValues(metadata.Map{"value": json.RawMessage(`"y"`)})
			if err != nil {
				return err
			}
			if err := s.client.HSet(ctx, s.key, metadataField, `{"value":"y"}`, termsField, tags).Err(); err != nil {
				return err
			}
		}
		return next(ctx, command)
	}
}

func TestLiveConditionalDeletionRetainsConcurrentWrite(t *testing.T) {
	store, client, prefix := liveStore(t, "COSINE")
	facts, err := metadata.FromValues(map[string]any{"value": "x"})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Upsert(t.Context(), fixtureBatch([]*document.Document{{ID: "one", Text: "text", Metadata: facts}})); err != nil {
		t.Fatal(err)
	}
	client.AddHook(&staleDeleteHook{client: client, key: prefix + "one"})
	if err = store.DeleteMatching(t.Context(), filter.EQ("value", "x"), fixtureBudget()); err == nil {
		t.Fatal("stale deletion reported success")
	}
	if value, readErr := client.HGet(t.Context(), prefix+"one", metadataField).Result(); readErr != nil || value != `{"value":"y"}` {
		t.Fatalf("concurrent metadata was deleted: %s, %v", value, readErr)
	}
	if err = store.DeleteMatching(t.Context(), filter.EQ("value", "y"), fixtureBudget()); err != nil {
		t.Fatal(err)
	}
}

func TestLiveRESP2RejectedBeforeUse(t *testing.T) {
	store, client, _ := liveStore(t, "COSINE")
	options := *client.Options()
	options.Protocol = 2
	resp2 := goredis.NewClient(&options)
	t.Cleanup(func() { _ = resp2.Close() })
	other, err := NewStore(t.Context(), StoreConfig{Client: resp2, ScanNodes: func(ctx context.Context, visit func(context.Context, *goredis.Client) error) error {
		return visit(ctx, resp2)
	}, IndexName: store.indexName, Space: fixtureSpace})
	if other != nil || !errors.Is(err, ErrIncompatibleIndex) {
		t.Fatalf("RESP2 was accepted without a warning channel: %#v, %v", other, err)
	}
}

func TestLiveNativeTagFilterAndHardByteGrant(t *testing.T) {
	s, _, _ := liveStore(t, "COSINE")
	docs := []*document.Document{{ID: "scalar", Text: "text", Metadata: metadata.Map{"v": json.RawMessage(`"A|x},"`)}}, {ID: "array", Text: "text", Metadata: metadata.Map{"v": json.RawMessage(`["A|x},"]`)}}}
	origin := document.NewProvenance("urn:manual", []byte("original document"))
	docs[0].Provenance = origin.Split("chapter", 2, 4)
	if err := s.Upsert(t.Context(), fixtureBatch(docs)); err != nil {
		t.Fatal(err)
	}
	if err := awaitVisibleDocuments(t.Context(), s, docs); err != nil {
		t.Fatal(err)
	}
	q := &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("q"), Options: vectorstore.SearchOptions{Filter: filter.EQ("v", "A|x},")}}
	r, err := s.Query(t.Context(), q)
	if err != nil || len(r.Results) != 1 || r.Results[0].Document.ID != "scalar" {
		t.Fatalf("raw native filter = %v %v", r, err)
	}
	found := r.Results[0].Document
	if found.Provenance == nil || found.Provenance.Revision != origin.Revision || len(found.Provenance.Chunks) != 1 || found.Provenance.Chunks[0] != docs[0].Provenance.Chunks[0] {
		t.Fatalf("lost source revision or fragment: %+v", found.Provenance)
	}
	if _, leaked := found.Metadata[document.StoredProvenanceKey]; leaked {
		t.Fatal("a native storage key entered the business metadata projection")
	}
	r, err = s.QueryByScan(t.Context(), q, vectorstore.ScanBudget{MaxRecords: 10, MaxBytes: 1, Timeout: time.Second})
	if !errors.Is(err, vectorstore.ErrScanBudget) || r != nil {
		t.Fatalf("exhausted grant=%v %v", r, err)
	}
}
