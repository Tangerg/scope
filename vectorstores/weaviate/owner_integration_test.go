//go:build integration

package weaviate

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
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
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
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

func liveWeaviateStore(t *testing.T, metric string, transport http.RoundTripper) (*Store, *weaviateclient.Client) {
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
	store, err := NewStore(t.Context(), StoreConfig{Client: client, ClassName: name, Space: fixtureSpace})
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
		response, err := fixtureQuery(ctx, store, &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("filter conformance"), Options: vectorstore.SearchOptions{TopK: len(docs)}})
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

func TestLiveNativeFilterAcrossModesAndMetrics(t *testing.T) {
	docs := []*document.Document{{ID: "00000000-0000-4000-8000-000000000001", Text: "native text", Metadata: metadata.Map{"v": json.RawMessage(`"X"`)}}, {ID: "00000000-0000-4000-8000-000000000002", Text: "native text", Metadata: metadata.Map{"v": json.RawMessage(`["X"]`)}}}
	for _, metric := range []string{distanceCosine, distanceDot, distanceL2Squared, distanceHamming, distanceManhattan} {
		t.Run(metric, func(t *testing.T) {
			s, c := liveWeaviateStore(t, metric, nil)
			if err := s.Upsert(t.Context(), fixtureBatch(docs)); err != nil {
				t.Fatal(err)
			}
			if err := awaitWeaviateDocuments(t.Context(), s, docs); err != nil {
				t.Fatal(err)
			}
			for _, mode := range []vectorstore.SearchMode{vectorstore.SearchModeSemantic, vectorstore.SearchModeHybrid} {
				q := &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("q"), Options: vectorstore.SearchOptions{Mode: mode, Filter: filter.EQ("v", "X")}}
				r, err := fixtureQuery(t.Context(), s, q)
				if err != nil || len(r.Results) != 1 || r.Results[0].Document.ID != docs[0].ID {
					t.Fatalf("native reply=%v err=%v", r, err)
				}
				q.Options.Filter = filter.EQ("v", 1)
				if r, err = fixtureQuery(t.Context(), s, q); r != nil || !errors.Is(err, vectorstore.ErrUnsupportedFilter) {
					t.Fatalf("unsupported=%v err=%v", r, err)
				}
			}
			if err := s.DeleteIDs(t.Context(), []string{docs[0].ID}); err != nil {
				t.Fatal(err)
			}
			remaining, err := c.Data().ObjectsGetter().WithClassName(s.className).WithLimit(2).Do(t.Context())
			if err != nil || len(remaining) != 1 || remaining[0].ID.String() != docs[1].ID {
				t.Fatalf("remaining=%v err=%v", remaining, err)
			}
		})
	}
}
func TestLiveExactMetadata(t *testing.T) {
	store, _ := liveWeaviateStore(t, distanceCosine, nil)
	var docs []*document.Document
	for i, facts := range []metadata.Map{nil, {}, {"huge": json.RawMessage(`1e1000`), "long": json.RawMessage(`1.00000000000000001`), "$native.key": json.RawMessage(`{"nested":[null,{},9007199254740993]}`), "content": json.RawMessage(`"business text"`)}} {
		docs = append(docs, &document.Document{ID: fmt.Sprintf("00000000-0000-4000-8000-%012d", i+1), Text: "native text🙂", Metadata: facts})
	}
	if err := store.Upsert(t.Context(), fixtureBatch(docs)); err != nil {
		t.Fatal(err)
	}
	if err := awaitWeaviateDocuments(t.Context(), store, docs); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []vectorstore.SearchMode{vectorstore.SearchModeSemantic, vectorstore.SearchModeHybrid} {
		response, err := fixtureQuery(t.Context(), store, &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("native text"), Options: vectorstore.SearchOptions{TopK: len(docs), Mode: mode}})
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
	mu     sync.Mutex
	groups []int
}

func (l *liveWeaviateTransport) RoundTrip(request *http.Request) (*http.Response, error) {
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

func TestLiveStrictSource(t *testing.T) {
	trace := &liveWeaviateTransport{}
	store, _ := liveWeaviateStore(t, distanceCosine, trace)
	client := liveWeaviateClient(t, nil)
	first, second := "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000002"
	docs := []*document.Document{{ID: first, Text: "text", Metadata: metadata.Map{"value": json.RawMessage(`"x"`)}}, {ID: second, Text: "text", Metadata: metadata.Map{"value": json.RawMessage(`"x"`)}}}
	if err := store.Upsert(t.Context(), fixtureBatch(docs)); err != nil {
		t.Fatal(err)
	}
	if err := client.Data().Updater().WithClassName(store.className).WithID(first).WithMerge().WithProperties(map[string]any{fieldMetadata: `{"key":1,"key":2}`}).Do(t.Context()); err != nil {
		t.Fatal(err)
	}
	response, err := fixtureQuery(t.Context(), store, &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("q"), Options: vectorstore.SearchOptions{Filter: filter.EQ("value", "x"), MinScore: 1}})
	if err == nil || response != nil {
		t.Fatalf("native corruption became success: %v %v", response, err)
	}
}

func TestLiveRawDistanceGroupsAndSingleHybridFusion(t *testing.T) {
	trace := &liveWeaviateTransport{}
	store, _ := liveWeaviateStore(t, distanceDot, trace)
	var docs []*document.Document
	for i := range 257 {
		text := "other"
		if i == 256 {
			text = "best"
		}
		docs = append(docs, &document.Document{ID: fmt.Sprintf("00000000-0000-4000-8000-%012d", i+1), Text: text, Metadata: metadata.Map{"value": json.RawMessage(`"x"`)}})
	}
	batch := fixtureBatch(docs)
	for i, r := range batch.Records {
		batch.Records[i].Values = []float64{40, 0}
		if strings.Contains(r.Document.Text, "best") {
			batch.Records[i].Values = []float64{50, 0}
		}
	}
	if err := store.Upsert(t.Context(), batch); err != nil {
		t.Fatal(err)
	}
	if err := awaitWeaviateDocuments(t.Context(), store, docs); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []vectorstore.SearchMode{vectorstore.SearchModeSemantic, vectorstore.SearchModeHybrid} {
		trace.groups = nil
		response, err := fixtureQuery(t.Context(), store, &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("q"), Options: vectorstore.SearchOptions{TopK: 1, Filter: filter.EQ("value", "x"), Mode: mode}})
		want := []int{1}
		if err != nil || len(response.Results) != 1 || response.Results[0].Document.ID != docs[len(docs)-1].ID || !slices.Equal(trace.groups, want) {
			t.Fatalf("mode=%s groups=%v response=%v error=%v", mode, trace.groups, response, err)
		}
		if mode == vectorstore.SearchModeSemantic && response.Results[0].Score != 1 {
			t.Fatalf("expected saturated native dot score: %v", response.Results[0])
		}
	}
}
