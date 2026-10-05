//go:build integration

package elasticsearch

import (
	"context"
	"crypto/rand"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	elasticsearchsdk "github.com/elastic/go-elasticsearch/v8"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
	"github.com/Tangerg/scope/core/vectorstore/storetest"
)

func liveStore(t *testing.T, metric string, model embedding.Model) (*Store, *elasticsearchsdk.Client) {
	t.Helper()
	endpoint := os.Getenv("SCOPE_ELASTICSEARCH_ENDPOINT")
	if endpoint == "" {
		t.Fatal("SCOPE_ELASTICSEARCH_ENDPOINT is required with -tags=integration")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	t.Cleanup(transport.CloseIdleConnections)
	client, err := elasticsearchsdk.NewClient(elasticsearchsdk.Config{Addresses: []string{endpoint}, Username: os.Getenv("SCOPE_ELASTICSEARCH_USERNAME"), Password: os.Getenv("SCOPE_ELASTICSEARCH_PASSWORD"), Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 5*time.Second)
		defer cancel()
		if closeErr := client.Close(ctx); closeErr != nil {
			t.Error(closeErr)
		}
	})
	name := "scope-es-" + strings.ToLower(rand.Text())
	body := fmt.Sprintf(`{"mappings":{"dynamic":"strict","properties":{"content":{"type":"text"},"metadata_json":{"type":"keyword","index":false,"doc_values":false},"embedding":{"type":"dense_vector","dims":2,"similarity":%q,"index_options":{"type":"flat"}}}}}`, metric)
	created, err := client.Indices.Create(name, client.Indices.Create.WithContext(t.Context()), client.Indices.Create.WithBody(strings.NewReader(body)))
	if err != nil {
		t.Fatal(err)
	}
	if created.IsError() {
		raw, readErr := io.ReadAll(created.Body)
		closeErr := created.Body.Close()
		t.Fatalf("native index creation: status=%d body=%s read=%v close=%v", created.StatusCode, raw, readErr, closeErr)
	}
	if err = created.Body.Close(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 10*time.Second)
		defer cancel()
		response, deleteErr := client.Indices.Delete([]string{name}, client.Indices.Delete.WithContext(ctx))
		if deleteErr != nil {
			t.Error(deleteErr)
			return
		}
		if response.IsError() {
			t.Errorf("native index cleanup: HTTP %d", response.StatusCode)
		}
		if closeErr := response.Body.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	})
	store, err := NewStore(t.Context(), StoreConfig{Client: client, IndexName: name, EmbeddingModel: model, DocumentBatcher: testBatcher{size: 32}})
	if err != nil {
		t.Fatal(err)
	}
	return store, client
}

func refreshNative(ctx context.Context, client *elasticsearchsdk.Client, name string) (err error) {
	response, err := client.Indices.Refresh(client.Indices.Refresh.WithContext(ctx), client.Indices.Refresh.WithIndex(name))
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, response.Body.Close()) }()
	if response.IsError() {
		return fmt.Errorf("native refresh: HTTP %d", response.StatusCode)
	}
	return nil
}

func TestLiveFilterConformance(t *testing.T) {
	store, client := liveStore(t, "cosine", constantModel())
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
		if err := refreshNative(ctx, client, store.indexName); err != nil {
			return nil, err
		}
		response, err := store.Search(ctx, &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{TopK: len(docs), Filter: predicate}})
		if err != nil {
			return nil, err
		}
		var ids []string
		for _, hit := range response.Results {
			ids = append(ids, hit.Document.ID)
		}
		return ids, nil
	}})
}

func TestLiveExactMetadataAndNativeMetrics(t *testing.T) {
	for _, metric := range []string{"cosine", "l2_norm", "dot_product", "max_inner_product"} {
		t.Run(metric, func(t *testing.T) {
			store, client := liveStore(t, metric, constantModel())
			facts := []metadata.Map{nil, {}, {"content": json.RawMessage(`"user value"`), "embedding": json.RawMessage(`{"nested":[1,null]}`), "metadata_json": json.RawMessage(`9007199254740993`), "decimal": json.RawMessage(`1.0000000000000000001`), "huge": json.RawMessage(`1e1000`)}}
			docs := make([]*document.Document, len(facts))
			for i := range facts {
				docs[i] = &document.Document{ID: fmt.Sprintf(" spaced / * ? 中文🙂 %d ", i), Text: "text", Metadata: facts[i]}
			}
			if err := store.Index(t.Context(), &vectorstore.IndexRequest{Documents: docs}); err != nil {
				t.Fatal(err)
			}
			if refreshErr := refreshNative(t.Context(), client, store.indexName); refreshErr != nil {
				t.Fatal(refreshErr)
			}
			response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{TopK: len(docs)}})
			if err != nil {
				t.Fatal(err)
			}
			for _, doc := range docs {
				found := slices.IndexFunc(response.Results, func(hit *vectorstore.SearchResult) bool { return hit.Document.ID == doc.ID })
				if found < 0 || !doc.Metadata.Equal(response.Results[found].Document.Metadata) || (doc.Metadata == nil) != (response.Results[found].Document.Metadata == nil) {
					t.Fatalf("native round trip changed %q", doc.ID)
				}
			}
			if err = store.DeleteWhere(t.Context(), filter.IsNull("unused")); err != nil {
				t.Fatal(err)
			}
			if refreshErr := refreshNative(t.Context(), client, store.indexName); refreshErr != nil {
				t.Fatal(refreshErr)
			}
			if response, err = store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query"}); err != nil || len(response.Results) != 0 {
				t.Fatalf("conditional deletion retained documents: %#v, %v", response, err)
			}
		})
	}
}

func TestLiveNativeEnvelope(t *testing.T) {
	store, client := liveStore(t, "cosine", constantModel())
	if err := store.Index(t.Context(), &vectorstore.IndexRequest{Documents: []*document.Document{{ID: "one", Text: "text"}}}); err != nil {
		t.Fatal(err)
	}
	if err := refreshNative(t.Context(), client, store.indexName); err != nil {
		t.Fatal(err)
	}
	response, err := client.Search(client.Search.WithContext(t.Context()), client.Search.WithIndex(store.indexName), client.Search.WithBody(strings.NewReader(`{"size":1,"_source":true,"seq_no_primary_term":true,"stored_fields":["_routing"],"track_total_hits":true,"knn":{"field":"embedding","query_vector":[1,0],"k":1}}`)))
	if err != nil {
		t.Fatal(err)
	}
	var page searchResponse
	decodeErr := jsonv2.UnmarshalRead(response.Body, &page)
	closeErr := response.Body.Close()
	if response.IsError() || decodeErr != nil || closeErr != nil {
		t.Fatalf("native envelope: HTTP %d, decode=%v, close=%v", response.StatusCode, decodeErr, closeErr)
	}
	if err = page.validate(store.indexName); err != nil {
		t.Fatal(err)
	}
	if len(*page.Hits.Hits) != 1 {
		t.Fatal("native envelope lost hits")
	}
}
