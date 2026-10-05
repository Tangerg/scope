//go:build integration

package weaviate

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	weaviateclient "github.com/weaviate/weaviate-go-client/v5/weaviate"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
	"github.com/Tangerg/scope/core/vectorstore/storetest"
)

func liveWeaviateClient(t *testing.T, transport http.RoundTripper) *weaviateclient.Client {
	t.Helper()
	endpoint := os.Getenv("SCOPE_WEAVIATE_URL")
	if endpoint == "" {
		t.Fatal("SCOPE_WEAVIATE_URL is required with -tags=integration")
	}
	address, err := url.Parse(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	client, err := weaviateclient.NewClient(weaviateclient.Config{Host: address.Host, Scheme: address.Scheme, ConnectionClient: &http.Client{Transport: transport, Timeout: 30 * time.Second}})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func liveWeaviateStore(t *testing.T, metric string, vectorFor func(string) []float64, transport http.RoundTripper) (*Store, *weaviateclient.Client) {
	t.Helper()
	client := liveWeaviateClient(t, transport)
	name := "ScopeNative_" + rand.Text()
	class := nativeClass(name, metric)
	// Exact native retrieval makes corpus membership and raw-distance order deterministic.
	class.VectorIndexType = "flat"
	if err := client.Schema().ClassCreator().WithClass(class).Do(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 30*time.Second)
		defer cancel()
		if err := client.Schema().ClassDeleter().WithClassName(name).Do(ctx); err != nil {
			t.Error(err)
		}
	})
	model := embedding.ModelFunc(func(_ context.Context, request *embedding.Request) (*embedding.Response, error) {
		outputs := make([]*embedding.Output, len(request.Texts))
		for i, text := range request.Texts {
			vector := []float64{1, 0}
			if vectorFor != nil {
				vector = vectorFor(text)
			}
			outputs[i] = &embedding.Output{Embedding: vector}
		}
		return embedding.NewResponse(outputs, nil)
	})
	store, err := NewStore(t.Context(), StoreConfig{Client: client, ClassName: name, EmbeddingModel: model, DocumentBatcher: testBatcher{}})
	if err != nil {
		t.Fatal(err)
	}
	return store, client
}

func awaitWeaviateDocuments(ctx context.Context, store *Store, docs []*document.Document) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	visibleCount := 0
	for {
		response, err := store.Search(ctx, &vectorstore.SearchRequest{Query: "filter conformance", Options: vectorstore.SearchOptions{TopK: len(docs)}})
		if err != nil {
			return fmt.Errorf("fixture visibility: %d of %d hits: %w", visibleCount, len(docs), err)
		}
		visibleCount = len(response.Results)
		visible := len(response.Results) == len(docs)
		for _, doc := range docs {
			visible = visible && slices.ContainsFunc(response.Results, func(hit *vectorstore.SearchResult) bool {
				return hit.Document.ID == doc.ID && hit.Document.Metadata.Equal(doc.Metadata)
			})
		}
		if visible {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("fixture visibility: %d of %d hits: %w", visibleCount, len(docs), ctx.Err())
		case <-ticker.C:
		}
	}
}

func TestLiveFilterConformance(t *testing.T) {
	for _, metric := range []string{distanceCosine, distanceDot, distanceL2Squared, distanceHamming, distanceManhattan} {
		t.Run(metric, func(t *testing.T) {
			for _, mode := range []vectorstore.SearchMode{vectorstore.SearchModeSemantic, vectorstore.SearchModeHybrid} {
				t.Run(string(mode), func(t *testing.T) {
					store, client := liveWeaviateStore(t, metric, nil, nil)
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
						if err := awaitWeaviateDocuments(ctx, store, docs); err != nil {
							return nil, err
						}
						response, err := store.Search(ctx, &vectorstore.SearchRequest{Query: "filter conformance", Options: vectorstore.SearchOptions{TopK: len(docs), Mode: mode, Filter: predicate}})
						if err != nil {
							return nil, err
						}
						var ids []string
						for _, hit := range response.Results {
							ids = append(ids, hit.Document.ID)
						}
						if err = store.DeleteWhere(ctx, predicate); err != nil {
							return nil, err
						}
						remaining, err := client.Data().ObjectsGetter().WithClassName(store.className).WithLimit(len(docs)).Do(ctx)
						if err != nil {
							return nil, err
						}
						if len(remaining) != len(docs)-len(ids) {
							return nil, fmt.Errorf("native deletion changed unexpected identities: %d", len(remaining))
						}
						for _, object := range remaining {
							if slices.Contains(ids, object.ID.String()) {
								return nil, fmt.Errorf("native deletion retained %s", object.ID)
							}
						}
						return ids, nil
					}})
				})
			}
		})
	}
}

func TestLiveExactMetadata(t *testing.T) {
	store, _ := liveWeaviateStore(t, distanceCosine, nil, nil)
	var docs []*document.Document
	for i, facts := range []metadata.Map{nil, {}, {"huge": json.RawMessage(`1e1000`), "long": json.RawMessage(`1.00000000000000001`), "$native.key": json.RawMessage(`{"nested":[null,{},9007199254740993]}`), "content": json.RawMessage(`"business text"`)}} {
		docs = append(docs, &document.Document{ID: fmt.Sprintf("00000000-0000-4000-8000-%012d", i+1), Text: "native text🙂", Metadata: facts})
	}
	if err := store.Index(t.Context(), &vectorstore.IndexRequest{Documents: docs}); err != nil {
		t.Fatal(err)
	}
	if err := awaitWeaviateDocuments(t.Context(), store, docs); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []vectorstore.SearchMode{vectorstore.SearchModeSemantic, vectorstore.SearchModeHybrid} {
		response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "native text", Options: vectorstore.SearchOptions{TopK: len(docs), Mode: mode}})
		if err != nil || len(response.Results) != len(docs) {
			t.Fatalf("response=%v error=%v", response, err)
		}
		for _, doc := range docs {
			if !slices.ContainsFunc(response.Results, func(hit *vectorstore.SearchResult) bool {
				return hit.Document.ID == doc.ID && hit.Document.Text == doc.Text && hit.Document.Metadata.Equal(doc.Metadata) && (hit.Document.Metadata == nil) == (doc.Metadata == nil)
			}) {
				t.Fatalf("native JSON changed: %v", doc)
			}
		}
	}
}

type liveWeaviateTransport struct {
	mu           sync.Mutex
	beforeDelete func(context.Context) error
	groups       []int
}

func (l *liveWeaviateTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	l.mu.Lock()
	before := l.beforeDelete
	if request.Method == http.MethodDelete && request.URL.Path == "/v1/batch/objects" {
		l.beforeDelete = nil
	} else {
		before = nil
	}
	l.mu.Unlock()
	if before != nil {
		if err := before(request.Context()); err != nil {
			return nil, err
		}
	}
	if request.Method == http.MethodPost && request.URL.Path == "/v1/graphql" {
		raw, err := io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
		if err = request.Body.Close(); err != nil {
			return nil, err
		}
		request.Body = io.NopCloser(bytes.NewReader(raw))
		var body struct {
			Query string `json:"query"`
		}
		if err = jsonv2.Unmarshal(raw, &body); err != nil {
			return nil, err
		}
		if found := regexp.MustCompile(`valueText:\s*(\[[^\]]*\])`).FindStringSubmatch(body.Query); found != nil {
			var ids []string
			if err = jsonv2.Unmarshal([]byte(found[1]), &ids); err != nil {
				return nil, err
			}
			l.mu.Lock()
			l.groups = append(l.groups, len(ids))
			l.mu.Unlock()
		}
	}
	return http.DefaultTransport.RoundTrip(request)
}

func TestLiveConditionalDeletionAndStrictSource(t *testing.T) {
	trace := &liveWeaviateTransport{}
	store, _ := liveWeaviateStore(t, distanceCosine, nil, trace)
	client := liveWeaviateClient(t, nil)
	first, second := "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000002"
	docs := []*document.Document{{ID: first, Text: "text", Metadata: metadata.Map{"value": json.RawMessage(`"x"`)}}, {ID: second, Text: "text", Metadata: metadata.Map{"value": json.RawMessage(`"x"`)}}}
	if err := store.Index(t.Context(), &vectorstore.IndexRequest{Documents: docs}); err != nil {
		t.Fatal(err)
	}
	trace.beforeDelete = func(ctx context.Context) error {
		return client.Data().Updater().WithClassName(store.className).WithID(first).WithMerge().WithProperties(map[string]any{fieldMetadata: `{"value":"X"}`}).Do(ctx)
	}
	if err := store.DeleteWhere(t.Context(), filter.EQ("value", "x")); err != nil {
		t.Fatal(err)
	}
	objects, err := client.Data().ObjectsGetter().WithClassName(store.className).WithVector().WithLimit(10).Do(t.Context())
	if err != nil || len(objects) != 1 || objects[0].ID.String() != first || objects[0].Properties.(map[string]any)[fieldMetadata] != `{"value":"X"}` {
		t.Fatalf("changed fact lost: %v %v", objects, err)
	}
	if err = client.Data().Updater().WithClassName(store.className).WithID(first).WithMerge().WithProperties(map[string]any{fieldMetadata: `{"key":1,"key":2}`}).Do(t.Context()); err != nil {
		t.Fatal(err)
	}
	response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "q", Options: vectorstore.SearchOptions{Filter: filter.EQ("value", "outside"), MinScore: 1}})
	if err == nil || response != nil {
		t.Fatalf("native corruption became success: %v %v", response, err)
	}
}

func TestLiveRawDistanceGroupsAndSingleHybridFusion(t *testing.T) {
	trace := &liveWeaviateTransport{}
	store, _ := liveWeaviateStore(t, distanceDot, func(text string) []float64 {
		if text == "q" {
			return []float64{1, 0}
		}
		if strings.Contains(text, "best") {
			return []float64{50, 0}
		}
		return []float64{40, 0}
	}, trace)
	var docs []*document.Document
	for i := range metadataScanPageSize + 1 {
		text := "other"
		if i == metadataScanPageSize {
			text = "best"
		}
		docs = append(docs, &document.Document{ID: fmt.Sprintf("00000000-0000-4000-8000-%012d", i+1), Text: text, Metadata: metadata.Map{"value": json.RawMessage(`"x"`)}})
	}
	if err := store.Index(t.Context(), &vectorstore.IndexRequest{Documents: docs}); err != nil {
		t.Fatal(err)
	}
	if err := awaitWeaviateDocuments(t.Context(), store, docs); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []vectorstore.SearchMode{vectorstore.SearchModeSemantic, vectorstore.SearchModeHybrid} {
		trace.groups = nil
		response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "q", Options: vectorstore.SearchOptions{TopK: 1, Filter: filter.EQ("value", "x"), Mode: mode}})
		want := []int{metadataScanPageSize, 1}
		if mode == vectorstore.SearchModeHybrid {
			want = []int{len(docs)}
		}
		if err != nil || len(response.Results) != 1 || response.Results[0].Document.ID != docs[len(docs)-1].ID || !slices.Equal(trace.groups, want) {
			t.Fatalf("mode=%s groups=%v response=%v error=%v", mode, trace.groups, response, err)
		}
		if mode == vectorstore.SearchModeSemantic && response.Results[0].Score != 1 {
			t.Fatalf("expected saturated native dot score: %v", response.Results[0])
		}
	}
}
