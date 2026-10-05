//go:build integration

package azureaisearch

import (
	"context"
	"crypto/rand"
	jsonv2 "encoding/json/v2"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
	"github.com/Tangerg/scope/core/vectorstore/storetest"
)

type nativeAuthentication struct {
	origin, key string
	base        http.RoundTripper
}

func (n nativeAuthentication) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.URL.Scheme+"://"+request.URL.Host != n.origin {
		return nil, fmt.Errorf("native fixture refused credential target %s", request.URL.Host)
	}
	authenticated := request.Clone(request.Context())
	authenticated.Header.Set("api-key", n.key)
	return n.base.RoundTrip(authenticated)
}

type nativeFixture struct {
	store *Store
	ids   []string
}

func newNativeFixture(t *testing.T, metric string) *nativeFixture {
	t.Helper()
	endpoint, key := os.Getenv("SCOPE_AZURE_SEARCH_ENDPOINT"), os.Getenv("SCOPE_AZURE_SEARCH_API_KEY")
	if endpoint == "" || key == "" {
		t.Fatal("SCOPE_AZURE_SEARCH_ENDPOINT and SCOPE_AZURE_SEARCH_API_KEY are required; the fixture creates and removes one unique index")
	}
	endpoint = strings.TrimRight(endpoint, "/")
	index := "scope-native-" + strings.ToLower(rand.Text())
	transport := http.DefaultTransport.(*http.Transport).Clone()
	t.Cleanup(transport.CloseIdleConnections)
	client := &http.Client{Timeout: 30 * time.Second, Transport: nativeAuthentication{origin: endpoint, key: key, base: transport}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	var schema map[string]any
	if err := jsonv2.Unmarshal([]byte(nativeSchema(metric)), &schema); err != nil {
		t.Fatal(err)
	}
	schema["name"] = index
	schema["vectorSearch"] = map[string]any{
		"profiles":   []any{map[string]any{"name": "p", "algorithm": "a"}},
		"algorithms": []any{map[string]any{"name": "a", "kind": "exhaustiveKnn", "exhaustiveKnnParameters": map[string]any{"metric": metric}}},
	}
	wire := &Store{endpoint: endpoint, indexName: index, apiVersion: DefaultAPIVersion, httpClient: client, maxResponseBytes: DefaultMaxResponseBytes}
	if _, err := wire.sendJSON(t.Context(), http.MethodPost, "/indexes", schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := wire.sendJSON(ctx, http.MethodDelete, "/indexes/"+index, nil); err != nil {
			t.Errorf("remove owned native index %s: %v", index, err)
		}
	})
	model := embedding.ModelFunc(func(_ context.Context, request *embedding.Request) (*embedding.Response, error) {
		outputs := make([]*embedding.Output, len(request.Texts))
		for i, text := range request.Texts {
			vector := []float64{1, 0}
			if text == "opposite" {
				vector = []float64{-1, 0}
			}
			outputs[i] = &embedding.Output{Embedding: vector}
		}
		return embedding.NewResponse(outputs, nil)
	})
	store, err := NewStore(t.Context(), StoreConfig{Endpoint: endpoint, IndexName: index, HTTPClient: client, EmbeddingModel: model, DocumentBatcher: writeTestBatcher{}})
	if err != nil {
		t.Fatal(err)
	}
	return &nativeFixture{store: store}
}

func (n *nativeFixture) wait(ctx context.Context, want []string) error {
	wanted := slices.Clone(want)
	slices.Sort(wanted)
	waitCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		got, err := n.store.selectIDs(waitCtx, nil)
		if err != nil {
			return err
		}
		slices.Sort(got)
		if slices.Equal(got, wanted) {
			return nil
		}
		select {
		case <-waitCtx.Done():
			return waitCtx.Err()
		case <-ticker.C:
		}
	}
}

func (n *nativeFixture) replace(ctx context.Context, docs []*document.Document) error {
	if err := n.store.DeleteIDs(ctx, n.ids); err != nil {
		return err
	}
	if err := n.wait(ctx, nil); err != nil {
		return err
	}
	if err := n.store.Index(ctx, &vectorstore.IndexRequest{Documents: docs}); err != nil {
		return err
	}
	n.ids = make([]string, len(docs))
	for i, doc := range docs {
		n.ids[i] = doc.ID
	}
	return n.wait(ctx, n.ids)
}

func TestNativeCoreFilterConformance(t *testing.T) {
	fixture := newNativeFixture(t, "cosine")
	storetest.FilterConformance(t, storetest.FilterConfig{Query: func(ctx context.Context, docs []*document.Document, predicate filter.Predicate) ([]string, error) {
		if err := fixture.replace(ctx, docs); err != nil {
			return nil, err
		}
		response, err := fixture.store.Search(ctx, &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{Filter: predicate, TopK: len(docs)}})
		if err != nil {
			return nil, err
		}
		ids := make([]string, len(response.Results))
		for i, row := range response.Results {
			ids[i] = row.Document.ID
		}
		return ids, nil
	}})
}

func TestNativeSchemaMetricsAndCoreMetadata(t *testing.T) {
	for _, metric := range []string{"cosine", "dotProduct", "euclidean"} {
		t.Run(metric, func(t *testing.T) {
			fixture := newNativeFixture(t, metric)
			docs := []*document.Document{
				{ID: "one", Text: "same", Metadata: rawMetadata(`{"large":9007199254740993,"nested":{"null":null},"precise":1.0000000000000000001,"huge":1e1000}`)},
				{ID: "two", Text: "opposite", Metadata: metadata.Map{}}, {ID: "three", Text: "same"},
			}
			if err := fixture.replace(t.Context(), docs); err != nil {
				t.Fatal(err)
			}
			response, err := fixture.store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{TopK: 3}})
			if err != nil || len(response.Results) != 3 {
				t.Fatalf("native response = %v, %v", response, err)
			}
			for _, row := range response.Results {
				original := docs[slices.IndexFunc(docs, func(doc *document.Document) bool { return doc.ID == row.Document.ID })]
				if !original.Metadata.Equal(row.Document.Metadata) || (original.Metadata == nil) != (row.Document.Metadata == nil) {
					t.Fatalf("native metadata = %v, want %v", row.Document.Metadata, original.Metadata)
				}
			}
			if fixture.store.metric != nativeMetric(metric) {
				t.Fatal("store overrode native metric")
			}
			if metric == "cosine" {
				opposite := response.Results[slices.IndexFunc(response.Results, func(row *vectorstore.SearchResult) bool { return row.Document.ID == "two" })]
				if opposite.Score > 1e-6 {
					t.Fatalf("native cosine opposite = %v", opposite.Score)
				}
			}
			if err = fixture.store.DeleteIDs(t.Context(), []string{"one", "one", "unknown"}); err != nil {
				t.Fatal(err)
			}
			if err = fixture.wait(t.Context(), []string{"two", "three"}); err != nil {
				t.Fatal(err)
			}
			if _, err = fixture.store.Search(t.Context(), &vectorstore.SearchRequest{Query: "same", Options: vectorstore.SearchOptions{Mode: vectorstore.SearchModeHybrid, TopK: 2}}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
