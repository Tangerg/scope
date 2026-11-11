//go:build integration

package opensearch

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Tangerg/scope/vectorstores/protocol/elastic"

	opensearchsdk "github.com/opensearch-project/opensearch-go/v5"

	"github.com/Tangerg/scope/core/document"

	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
	"github.com/Tangerg/scope/core/vectorstore/storetest"
)

func nativeCall(ctx context.Context, client *opensearchsdk.Client, method, path string, body []byte) (err error) {
	request, err := http.NewRequestWithContext(ctx, method, path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Stream(request)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, response.Body.Close()) }()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("native HTTP %d: %s", response.StatusCode, raw)
	}
	return nil
}

type liveConfig struct {
	engine string
	metric string

	encoder     string
	compression string
}

func liveStore(t *testing.T, config liveConfig) (*elastic.Store, *opensearchsdk.Client, string) {
	t.Helper()
	endpoint := os.Getenv("SCOPE_OPENSEARCH_ENDPOINT")
	if endpoint == "" {
		t.Fatal("SCOPE_OPENSEARCH_ENDPOINT is required with -tags=integration")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	t.Cleanup(transport.CloseIdleConnections)
	client, err := opensearchsdk.NewClient(opensearchsdk.Config{Addresses: []string{endpoint}, Username: os.Getenv("SCOPE_OPENSEARCH_USERNAME"), Password: os.Getenv("SCOPE_OPENSEARCH_PASSWORD"), Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := client.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	})
	name := "scope-os-" + strings.ToLower(rand.Text())
	encoder := ""
	if config.encoder != "" {
		encoder = `,"parameters":{"encoder":` + config.encoder + `}`
	}
	compression := ""
	if config.compression != "" {
		compression = fmt.Sprintf(`,"compression_level":%q`, config.compression)
	}
	body := fmt.Sprintf(`{"settings":{"index.knn":true,"index.knn.derived_source.enabled":false,"index.derived_source.enabled":false},"mappings":{"_meta":{"scope_vector_space":"paired-v1"},"dynamic":"strict","properties":{"content":{"type":"text"},"metadata_json":{"type":"keyword","index":false,"doc_values":false},"embedding":{"type":"knn_vector","dimension":2,"method":{"name":"hnsw","engine":%q,"space_type":%q%s}%s}}}}`, config.engine, config.metric, encoder, compression)
	if err = nativeCall(t.Context(), client, http.MethodPut, "/"+name, []byte(body)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 10*time.Second)
		defer cancel()
		if cleanupErr := nativeCall(ctx, client, http.MethodDelete, "/"+name, nil); cleanupErr != nil {
			t.Error(cleanupErr)
		}
	})
	store, err := NewStore(t.Context(), StoreConfig{Client: client, IndexName: name, Space: fixtureSpace})
	if err != nil {
		t.Fatal(err)
	}
	return store, client, name
}

func refreshNative(ctx context.Context, client *opensearchsdk.Client, name string) error {
	return nativeCall(ctx, client, http.MethodPost, "/"+name+"/_refresh", nil)
}

func TestLiveFilterConformance(t *testing.T) {
	for _, engine := range []string{"lucene", "faiss"} {
		t.Run(engine, func(t *testing.T) {
			store, client, index := liveStore(t, liveConfig{engine: engine, metric: "cosinesimil"})
			var previous []string
			storetest.FilterConformance(t, storetest.FilterConfig{Query: func(ctx context.Context, docs []*document.Document, predicate filter.Predicate) ([]string, error) {
				if err := store.DeleteIDs(ctx, previous); err != nil {
					return nil, err
				}
				previous = nil
				for _, doc := range docs {
					previous = append(previous, doc.ID)
				}
				if err := store.Upsert(ctx, fixtureBatch(docs)); err != nil {
					return nil, err
				}
				if err := refreshNative(ctx, client, index); err != nil {
					return nil, err
				}
				response, err := fixtureQuery(ctx, store, &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("query"), Options: vectorstore.SearchOptions{TopK: len(docs), Filter: predicate}})
				if err != nil {
					return nil, err
				}
				var ids []string
				for _, hit := range response.Results {
					ids = append(ids, hit.Document.ID)
				}
				return ids, nil
			}})
		})
	}
}

func TestLiveExactMetadataAndNativeMetrics(t *testing.T) {
	for _, engine := range []string{"lucene", "faiss"} {
		for _, metric := range []string{"cosinesimil", "l2", "innerproduct"} {
			t.Run(engine+"/"+metric, func(t *testing.T) {
				store, client, index := liveStore(t, liveConfig{engine: engine, metric: metric})
				facts := []metadata.Map{nil, {}, {"content": json.RawMessage(`"user value"`), "embedding": json.RawMessage(`{"nested":[1,null]}`), "metadata_json": json.RawMessage(`9007199254740993`), "decimal": json.RawMessage(`1.0000000000000000001`), "huge": json.RawMessage(`1e1000`)}}
				docs := make([]*document.Document, len(facts))
				for i := range facts {
					docs[i] = &document.Document{ID: fmt.Sprintf(" spaced / * ? 中文🙂 %d ", i), Text: "text", Metadata: facts[i]}
				}
				if err := store.Upsert(t.Context(), fixtureBatch(docs)); err != nil {
					t.Fatal(err)
				}
				if err := refreshNative(t.Context(), client, index); err != nil {
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
				}
				if err = store.DeleteMatching(t.Context(), filter.IsNull("unused"), fixtureBudget()); err != nil {
					t.Fatal(err)
				}
				if err = refreshNative(t.Context(), client, index); err != nil {
					t.Fatal(err)
				}
				if response, err = fixtureQuery(t.Context(), store, &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("query")}); err != nil || len(response.Results) != 0 {
					t.Fatalf("conditional deletion retained documents: %#v, %v", response, err)
				}
			})
		}
	}
}

func TestLiveFP16LimitsBelongToNativeIndexEncoding(t *testing.T) {
	for _, sample := range []struct {
		name, encoder, compression string
		clip                       bool
	}{
		{name: "explicit", encoder: `{"name":"sq","parameters":{"type":"fp16","clip":false}}`},
		{name: "automatic", compression: "2x"},
		{name: "native clipping", encoder: `{"name":"sq","parameters":{"type":"fp16","clip":true}}`, clip: true},
	} {
		t.Run(sample.name, func(t *testing.T) {
			store, client, index := liveStore(t, liveConfig{engine: "faiss", metric: "l2", encoder: sample.encoder, compression: sample.compression})

			batch := fixtureBatch([]*document.Document{{ID: "first", Text: "small"}, {ID: "second", Text: "large"}})
			batch.Records[1].Values = []float64{65505, 0}
			err := store.Upsert(t.Context(), batch)
			if sample.clip {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				if err == nil {
					t.Fatal("native FP16 boundary was ignored")
				}
				if err = refreshNative(t.Context(), client, index); err != nil {
					t.Fatal(err)
				}
				empty, queryErr := fixtureQuery(t.Context(), store, &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("small")})
				if queryErr != nil || len(empty.Results) != 0 {
					t.Fatalf("invalid second vector published the first: %#v, %v", empty, queryErr)
				}
				if err = store.Upsert(t.Context(), fixtureBatch([]*document.Document{{ID: "first", Text: "small"}})); err != nil {
					t.Fatal(err)
				}
			}
			if err = refreshNative(t.Context(), client, index); err != nil {
				t.Fatal(err)
			}
			response, err := fixtureQuery(t.Context(), store, &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("large")})
			if err != nil || len(response.Results) == 0 {
				t.Fatalf("native-valid FP32 query inherited index-only FP16 constraint: %#v, %v", response, err)
			}
		})
	}
}
