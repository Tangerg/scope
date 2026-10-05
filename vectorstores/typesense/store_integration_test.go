//go:build integration

package typesense

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/typesense/typesense-go/v3/typesense/api"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
	"github.com/Tangerg/scope/core/vectorstore/storetest"
)

func nativeFixture(t *testing.T) *Store {
	t.Helper()
	endpoint, key := os.Getenv("SCOPE_TYPESENSE_ENDPOINT"), os.Getenv("SCOPE_TYPESENSE_API_KEY")
	if endpoint == "" || key == "" {
		t.Fatal("SCOPE_TYPESENSE_ENDPOINT and SCOPE_TYPESENSE_API_KEY are required")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	t.Cleanup(transport.CloseIdleConnections)
	client, err := api.NewClient(endpoint, api.WithHTTPClient(&http.Client{Transport: transport, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}), api.WithRequestEditorFn(func(_ context.Context, request *http.Request) error {
		request.Header.Set("X-TYPESENSE-API-KEY", key)
		return nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("scope_test_%x", rand.Text())
	response, err := client.CreateCollection(t.Context(), api.CollectionSchema{Name: name, Fields: []api.Field{{Name: "content", Type: "string"}, {Name: "metadata", Type: "string", Index: new(false)}, {Name: "embedding", Type: "float[]", NumDim: new(2), VecDist: new("cosine")}}})
	checkNativeResponse(t, response, err, http.StatusCreated)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		deleted, deleteErr := client.DeleteCollection(ctx, name)
		checkNativeResponse(t, deleted, deleteErr, http.StatusOK)
	})
	store, err := NewStore(t.Context(), StoreConfig{Client: client, CollectionName: name, EmbeddingModel: constantModel(), DocumentBatcher: testBatcher{size: 32}})
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func checkNativeResponse(t *testing.T, response *http.Response, err error, status int) []byte {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	if response == nil || response.Body == nil {
		t.Fatal("native operation omitted response")
	}
	raw, readErr := io.ReadAll(io.LimitReader(response.Body, DefaultMaxResponseBytes+1))
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil || response.StatusCode != status || int64(len(raw)) > DefaultMaxResponseBytes {
		t.Fatalf("native operation: status=%d, read=%v, close=%v, body=%s", response.StatusCode, readErr, closeErr, raw)
	}
	return raw
}

func TestNativeFilterConformance(t *testing.T) {
	store := nativeFixture(t)
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
		response, err := store.Search(ctx, &vectorstore.SearchRequest{Query: "filter conformance", Options: vectorstore.SearchOptions{TopK: len(docs), Filter: predicate}})
		if err != nil {
			return nil, err
		}
		ids := make([]string, len(response.Results))
		for i, result := range response.Results {
			ids[i] = result.Document.ID
		}
		return ids, nil
	}})
}

func TestNativeMetadataIdentityPaginationAndReplacement(t *testing.T) {
	store := nativeFixture(t)
	docs := make([]*document.Document, 7)
	for i := range docs {
		docs[i] = &document.Document{ID: fmt.Sprintf("doc_%03d", i), Text: "searchable text", Metadata: metadata.Map{"number": json.RawMessage(`9007199254740993`), "nested": json.RawMessage(`{"decimal":1.0000000000000000001,"huge":1e1000}`)}}
	}
	for i, id := range []string{" spaced ", "*", "back`tick", "trailing\\", "a/b?x", "中文🙂"} {
		docs[i].ID = id
	}
	docs[0].Metadata = nil
	docs[1].Metadata = metadata.Map{}
	if err := store.Index(t.Context(), &vectorstore.IndexRequest{Documents: docs}); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []vectorstore.SearchMode{vectorstore.SearchModeSemantic, vectorstore.SearchModeHybrid} {
		for _, predicate := range []filter.Predicate{nil, filter.IsNull("unused")} {
			response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "searchable", Options: vectorstore.SearchOptions{TopK: len(docs), Mode: mode, Filter: predicate}})
			if err != nil {
				t.Fatal(err)
			}
			if len(response.Results) != len(docs) {
				t.Fatalf("mode=%s, got %d of %d", mode, len(response.Results), len(docs))
			}
			byID := make(map[string]*document.Document, len(docs))
			for _, result := range response.Results {
				byID[result.Document.ID] = result.Document
			}
			for _, doc := range docs {
				got := byID[doc.ID]
				if got == nil || got.Text != doc.Text || !got.Metadata.Equal(doc.Metadata) || (got.Metadata == nil) != (doc.Metadata == nil) {
					t.Fatalf("changed native roundtrip for %q: %#v", doc.ID, got)
				}
			}
		}
	}
	replacement := &document.Document{ID: docs[0].ID, Text: "replacement", Metadata: metadata.Map{"updated": json.RawMessage(`true`)}}
	if err := store.Index(t.Context(), &vectorstore.IndexRequest{Documents: []*document.Document{replacement}}); err != nil {
		t.Fatal(err)
	}
	predicate, err := filter.Parse("updated == true")
	if err != nil {
		t.Fatal(err)
	}
	response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "replacement", Options: vectorstore.SearchOptions{TopK: len(docs), Filter: predicate}})
	if err != nil || len(response.Results) != 1 || response.Results[0].Document.ID != replacement.ID || response.Results[0].Document.Text != replacement.Text {
		t.Fatalf("native replacement: response=%#v, error=%v", response, err)
	}
	ids := []string{"unknown", docs[0].ID}
	for _, doc := range docs {
		ids = append(ids, doc.ID)
	}
	if err = store.DeleteIDs(t.Context(), ids); err != nil {
		t.Fatal(err)
	}
	response, err = store.Search(t.Context(), &vectorstore.SearchRequest{Query: "searchable"})
	if err != nil || len(response.Results) != 0 {
		t.Fatalf("native deletion: response=%#v, error=%v", response, err)
	}
}

func TestNativePaginationPreservesRanking(t *testing.T) {
	store := nativeFixture(t)
	docs := make([]*document.Document, 300)
	for i := range docs {
		docs[i] = &document.Document{ID: fmt.Sprintf("doc_%03d", i), Text: "searchable text"}
	}
	if err := store.Index(t.Context(), &vectorstore.IndexRequest{Documents: docs}); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []vectorstore.SearchMode{vectorstore.SearchModeSemantic} {
		for _, filtered := range []bool{false, true} {
			params := api.MultiSearchCollectionParameters{Collection: new(store.collectionName), Q: new("*"), VectorQuery: new("embedding:([1,0], k: 300)"), PerPage: new(250)}
			var predicate filter.Predicate
			if filtered {
				predicate = filter.IsNull("unused")
				keys := make([]string, len(docs))
				for i, doc := range docs {
					keys[i] = base64.RawURLEncoding.EncodeToString([]byte(doc.ID))
				}
				params.FilterBy = new(keyFilter(keys))
			}
			var nativeIDs []string
			for page := 1; ; page++ {
				params.Page = new(page)
				query := struct {
					api.MultiSearchCollectionParameters `json:",inline"`
					EnableCurations                     bool `json:"enable_curations"`
				}{MultiSearchCollectionParameters: params}
				body := mustJSON(t, struct {
					Searches []any `json:"searches"`
				}{Searches: []any{query}})
				response, err := store.client.MultiSearchWithBody(t.Context(), nil, "application/json", bytes.NewReader(body))
				raw := checkNativeResponse(t, response, err, http.StatusOK)
				var wire struct {
					Results []struct {
						Found int `json:"found"`
						Hits  []struct {
							Document struct {
								ID string `json:"id"`
							} `json:"document"`
						} `json:"hits"`
					} `json:"results"`
				}
				if err = jsonv2.Unmarshal(raw, &wire); err != nil || len(wire.Results) != 1 {
					t.Fatalf("native pagination: %s, error=%v", raw, err)
				}
				for _, hit := range wire.Results[0].Hits {
					id, err := base64.RawURLEncoding.Strict().DecodeString(hit.Document.ID)
					if err != nil {
						t.Fatal(err)
					}
					nativeIDs = append(nativeIDs, string(id))
				}
				if len(nativeIDs) >= min(300, wire.Results[0].Found) {
					break
				}
				if len(wire.Results[0].Hits) == 0 {
					t.Fatal("native pagination omitted a page")
				}
			}
			if len(nativeIDs) <= MaxResultsPerPage {
				t.Fatal("native fixture did not exercise a second page")
			}
			seen := make(map[string]bool)
			for _, id := range nativeIDs {
				if seen[id] {
					t.Fatalf("native pagination repeated %q, mode=%s, filtered=%v", id, mode, filtered)
				}
				seen[id] = true
			}
			response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "searchable", Options: vectorstore.SearchOptions{Mode: mode, TopK: 300, Filter: predicate}})
			if err != nil {
				t.Fatalf("mode=%s, filtered=%v: %v", mode, filtered, err)
			}
			if len(response.Results) != len(nativeIDs) {
				t.Fatalf("got %d of %d native hits", len(response.Results), len(nativeIDs))
			}
			for i, result := range response.Results {
				if result.Document.ID != nativeIDs[i] {
					t.Fatalf("native rank %d: got %q, want %q", i, result.Document.ID, nativeIDs[i])
				}
			}
		}
	}
}
