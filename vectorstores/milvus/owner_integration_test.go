//go:build integration

package milvus

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/milvus-io/milvus-proto/go-api/v2/milvuspb"
	"github.com/milvus-io/milvus/client/v2/entity"
	"github.com/milvus-io/milvus/client/v2/index"
	"github.com/milvus-io/milvus/client/v2/milvusclient"
	"google.golang.org/grpc"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
	"github.com/Tangerg/scope/core/vectorstore/storetest"
)

func liveStore(t *testing.T, metric entity.MetricType, vectorFor func(string) []float64, interceptor grpc.UnaryClientInterceptor) (*Store, *milvusclient.Client) {
	t.Helper()
	address := os.Getenv("SCOPE_MILVUS_ADDRESS")
	if address == "" {
		t.Fatal("SCOPE_MILVUS_ADDRESS is required with -tags=integration")
	}
	config := &milvusclient.ClientConfig{Address: address}
	if interceptor != nil {
		config.DialOptions = []grpc.DialOption{grpc.WithUnaryInterceptor(interceptor)}
	}
	client, err := milvusclient.New(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 30*time.Second)
		defer cancel()
		if closeErr := client.Close(ctx); closeErr != nil {
			t.Error(closeErr)
		}
	})
	name := "scope_native_" + strings.ToLower(rand.Text())
	if err = client.CreateCollection(t.Context(), milvusclient.NewCreateCollectionOption(name, nativeSchema(name))); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 30*time.Second)
		defer cancel()
		if dropErr := client.DropCollection(ctx, milvusclient.NewDropCollectionOption(name)); dropErr != nil {
			t.Error(dropErr)
		}
	})
	// Native FLAT makes corpus membership and ranking deterministic.
	task, err := client.CreateIndex(t.Context(), milvusclient.NewCreateIndexOption(name, fieldVector, index.NewFlatIndex(metric)))
	if err != nil {
		t.Fatal(err)
	}
	if err = task.Await(t.Context()); err != nil {
		t.Fatal(err)
	}
	loaded, err := client.LoadCollection(t.Context(), milvusclient.NewLoadCollectionOption(name))
	if err != nil {
		t.Fatal(err)
	}
	if err = loaded.Await(t.Context()); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(t.Context(), StoreConfig{Client: client, CollectionName: name, EmbeddingModel: fixtureModel(vectorFor), DocumentBatcher: testBatcher{}})
	if err != nil {
		t.Fatal(err)
	}
	return store, client
}

func TestLiveFilterConformance(t *testing.T) {
	for _, metric := range []entity.MetricType{entity.COSINE, entity.IP, entity.L2} {
		t.Run(string(metric), func(t *testing.T) {
			store, _ := liveStore(t, metric, nil, nil)
			var previous []string
			storetest.FilterConformance(t, storetest.FilterConfig{Query: func(ctx context.Context, docs []*document.Document, predicate filter.Predicate) ([]string, error) {
				if err := store.DeleteIDs(ctx, previous); err != nil {
					return nil, err
				}
				previous = nil
				for _, doc := range docs {
					previous = append(previous, doc.ID)
				}
				if err := store.Index(ctx, &vectorstore.IndexRequest{Documents: docs}); err != nil {
					return nil, err
				}
				response, err := store.Search(ctx, &vectorstore.SearchRequest{Query: "filter conformance", Options: vectorstore.SearchOptions{Filter: predicate, TopK: len(docs)}})
				if err != nil {
					return nil, err
				}
				var ids []string
				for _, hit := range response.Results {
					ids = append(ids, hit.Document.ID)
				}
				if deleteErr := store.DeleteWhere(ctx, predicate); deleteErr != nil {
					return nil, deleteErr
				}
				policy, err := store.readPolicy(ctx)
				if err != nil {
					return nil, err
				}
				remaining, err := store.selectDocuments(ctx, policy, nil)
				if err != nil {
					return nil, err
				}
				if len(remaining) != len(docs)-len(ids) {
					return nil, fmt.Errorf("native deletion changed unexpected identities: %d", len(remaining))
				}
				for _, record := range remaining {
					if slices.Contains(ids, record.doc.ID) {
						return nil, fmt.Errorf("native deletion retained %s", record.doc.ID)
					}
				}
				return ids, nil
			}})
		})
	}
}

func TestLiveExactMetadataAndLiteralCursor(t *testing.T) {
	store, _ := liveStore(t, entity.COSINE, nil, nil)
	var docs []*document.Document
	facts := []metadata.Map{nil, {}, {"huge": json.RawMessage(`1e1000`), "decimal": json.RawMessage(`1.00000000000000001`), "$native.key": json.RawMessage(`{"nested":[null,{},9007199254740993]}`), "id": json.RawMessage(`"business ID"`)}}
	for position := range 35 {
		docs = append(docs, &document.Document{ID: fmt.Sprintf("id_%02d_\"\\🙂", position), Text: "native🙂", Metadata: facts[position%len(facts)]})
	}
	if err := store.Index(t.Context(), &vectorstore.IndexRequest{Documents: docs}); err != nil {
		t.Fatal(err)
	}
	response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "native", Options: vectorstore.SearchOptions{TopK: len(docs)}})
	if err != nil || len(response.Results) != len(docs) {
		t.Fatalf("response=%v error=%v", response, err)
	}
	for _, doc := range docs {
		if !slices.ContainsFunc(response.Results, func(hit *vectorstore.SearchResult) bool {
			return hit.Document.ID == doc.ID && hit.Document.Text == doc.Text && hit.Document.Metadata.Equal(doc.Metadata) && (hit.Document.Metadata == nil) == (doc.Metadata == nil)
		}) {
			t.Fatalf("native record changed: %v", doc)
		}
	}
	if deleteErr := store.DeleteWhere(t.Context(), mustPredicate(t, "id IS NOT NULL")); deleteErr != nil {
		t.Fatal(deleteErr)
	}
	response, err = store.Search(t.Context(), &vectorstore.SearchRequest{Query: "native", Options: vectorstore.SearchOptions{TopK: len(docs)}})
	if err != nil || len(response.Results) != len(docs)-11 {
		t.Fatalf("literal guarded deletion response=%v error=%v", response, err)
	}
}

type liveInterceptor struct {
	mu           sync.Mutex
	beforeDelete func(context.Context) error
	groups       []int
}

func (l *liveInterceptor) intercept(ctx context.Context, method string, request, reply any, connection *grpc.ClientConn, invoke grpc.UnaryInvoker, options ...grpc.CallOption) error {
	l.mu.Lock()
	before := l.beforeDelete
	if strings.HasSuffix(method, "/Delete") {
		l.beforeDelete = nil
	} else {
		before = nil
	}
	if query, ok := request.(*milvuspb.SearchRequest); ok {
		l.groups = append(l.groups, len(query.ExprTemplateValues["ids"].GetArrayVal().GetStringData().GetData()))
	}
	l.mu.Unlock()
	if before != nil {
		if err := before(ctx); err != nil {
			return err
		}
	}
	return invoke(ctx, method, request, reply, connection, options...)
}

func TestLiveDeletionKeepsChangedMetadata(t *testing.T) {
	interceptor := &liveInterceptor{}
	store, _ := liveStore(t, entity.IP, nil, interceptor.intercept)
	docs := []*document.Document{{ID: "one", Text: "original", Metadata: metadata.Map{"state": json.RawMessage(`"old"`)}}}
	if err := store.Index(t.Context(), &vectorstore.IndexRequest{Documents: docs}); err != nil {
		t.Fatal(err)
	}
	interceptor.beforeDelete = func(ctx context.Context) error {
		return store.Index(ctx, &vectorstore.IndexRequest{Documents: []*document.Document{{ID: "one", Text: "changed", Metadata: metadata.Map{"state": json.RawMessage(`"NEW"`)}}}})
	}
	if err := store.DeleteWhere(t.Context(), mustPredicate(t, "state == 'old'")); err != nil {
		t.Fatal(err)
	}
	response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "q"})
	if err != nil || len(response.Results) != 1 || response.Results[0].Document.Text != "changed" {
		t.Fatalf("updated native record deleted: %v %v", response, err)
	}
}

func TestLiveGroupsKeepNativeRawRank(t *testing.T) {
	interceptor := &liveInterceptor{}
	store, _ := liveStore(t, entity.IP, func(text string) []float64 {
		if text == "last" {
			return []float64{1e20, 0}
		}
		return []float64{1e10, 0}
	}, interceptor.intercept)
	var docs []*document.Document
	for position := range 129 {
		text := "first"
		if position == 128 {
			text = "last"
		}
		docs = append(docs, &document.Document{ID: fmt.Sprintf("id_%03d", position), Text: text})
	}
	if err := store.Index(t.Context(), &vectorstore.IndexRequest{Documents: docs}); err != nil {
		t.Fatal(err)
	}
	response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "q", Options: vectorstore.SearchOptions{TopK: 1}})
	if err != nil || len(response.Results) != 1 || response.Results[0].Document.ID != "id_128" {
		t.Fatalf("native raw rank changed: %v %v", response, err)
	}
	if !slices.Equal(interceptor.groups, []int{128, 1}) {
		t.Fatalf("native groups: %v", interceptor.groups)
	}
}
