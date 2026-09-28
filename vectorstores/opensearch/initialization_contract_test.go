package opensearch

import (
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	opensearchsdk "github.com/opensearch-project/opensearch-go/v4"
	"github.com/opensearch-project/opensearch-go/v4/opensearchapi"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
)

func TestNewStoreVerifiesCreatedAndExistingIndex(t *testing.T) {
	for _, existing := range []bool{false, true} {
		for _, sample := range []struct {
			name     string
			source   string
			settings string
			engine   string
			resolved string
			wantErr  string
		}{
			{name: "stored source", source: `{}`, engine: "lucene"},
			{name: "template disables source", source: `{"enabled":false}`, engine: "lucene", wantErr: "stored _source"},
			{name: "template prunes metadata", source: `{"excludes":["metadata.private"]}`, engine: "lucene", wantErr: "may prune"},
			{name: "template prunes content", source: `{"includes":["metadata"]}`, engine: "lucene", wantErr: "complete field"},
			{name: "template reconstructs source", source: `{"mode":"synthetic"}`, engine: "lucene", wantErr: "stored _source"},
			{name: "derived source", source: `{}`, settings: `{"index.derived_source.enabled":"true"}`, engine: "lucene", wantErr: "derived source"},
			{name: "unrelated analyzer arrays", source: `{}`, settings: `{"index.derived_source.enabled":"false","index.analysis.analyzer.custom.filter":["lowercase"]}`, engine: "lucene"},
			{name: "actual engine disagrees", source: `{}`, engine: "nmslib", wantErr: "uses engine"},
			{name: "engine is unknown", source: `{}`, engine: "future-engine", wantErr: "supported engine"},
			{name: "engine is unstated", source: `{}`, wantErr: "supported engine"},
			{name: "alias must preserve its boundary", source: `{}`, engine: "lucene", resolved: "tenant-data", wantErr: "only concrete indices"},
		} {
			t.Run(fmt.Sprintf("%s/existing=%v", sample.name, existing), func(t *testing.T) {
				created, mapped, settingsRead := false, false, false
				config := newHTTPStoreConfig(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					switch {
					case r.Method == http.MethodHead && r.URL.Path == "/documents":
						if !existing {
							w.WriteHeader(http.StatusNotFound)
						}
					case r.Method == http.MethodPut && r.URL.Path == "/documents":
						created = true
						fmt.Fprint(w, `{"acknowledged":true,"shards_acknowledged":true,"index":"documents"}`)
					case r.URL.Path == "/documents/_mapping":
						mapped = true
						if !existing && !created {
							t.Error("mapping read before creation")
						}
						name := "documents"
						if sample.resolved != "" {
							name = sample.resolved
						}
						fmt.Fprintf(w, `{%q:{"mappings":{"_source":%s,"properties":{"embedding":{"type":"knn_vector","dimension":2,"method":{"name":"hnsw","engine":%q,"space_type":"cosinesimil"}}}}}}`, name, sample.source, sample.engine)
					case r.URL.Path == "/documents/_settings":
						settingsRead = true
						if !mapped || r.URL.Query().Get("flat_settings") != "true" {
							t.Error("source settings request is not scoped after mapping validation")
						}
						settings := sample.settings
						if settings == "" {
							settings = `{}`
						}
						fmt.Fprintf(w, `{"documents":{"settings":%s}}`, settings)
					default:
						t.Errorf("unexpected constructor request %s %s", r.Method, r.URL)
						http.Error(w, "unexpected", http.StatusInternalServerError)
					}
				}))
				config.InitializeSchema = !existing
				store, err := NewStore(t.Context(), config)
				if sample.wantErr != "" {
					if !errors.Is(err, ErrIncompatibleIndex) || !strings.Contains(err.Error(), sample.wantErr) || store != nil {
						t.Fatalf("NewStore() = %v, %v; want incompatible %q", store, err, sample.wantErr)
					}
				} else if err != nil || store == nil || store.engine != EngineLucene || !settingsRead {
					t.Fatalf("NewStore() = %v, %v; source settings read=%v", store, err, settingsRead)
				}
				if created == existing || !mapped {
					t.Fatalf("created=%v mapped=%v existing=%v", created, mapped, existing)
				}
			})
		}
	}
}

func newHTTPStoreConfig(t *testing.T, handler http.Handler) StoreConfig {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := opensearchapi.NewClient(opensearchapi.Config{Client: opensearchsdk.Config{Addresses: []string{server.URL}, Transport: server.Client().Transport}})
	if err != nil {
		t.Fatal(err)
	}
	return StoreConfig{
		Client: client, IndexName: "documents", Dimensions: 2,
		EmbeddingModel: embedding.ModelFunc(func(context.Context, *embedding.Request) (*embedding.Response, error) {
			return &embedding.Response{Outputs: []*embedding.Output{{Embedding: []float64{1, 0}}}}, nil
		}),
		DocumentBatcher: new(contractBatcher),
	}
}

type contractBatcher struct{}

func (*contractBatcher) Batch(_ context.Context, docs []*document.Document) ([][]*document.Document, error) {
	return [][]*document.Document{docs}, nil
}

func newSearchContractStore(t *testing.T, engine Engine, space SpaceType, handler http.Handler) *Store {
	t.Helper()
	config := newHTTPStoreConfig(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodHead && r.URL.Path == "/documents":
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/documents/_mapping":
			fmt.Fprintf(w, `{"documents":{"mappings":{"properties":{"embedding":{"type":"knn_vector","dimension":2,"method":{"name":"hnsw","engine":%q,"space_type":%q}}}}}}`, engine, space)
		case r.URL.Path == "/documents/_settings":
			fmt.Fprint(w, `{"documents":{"settings":{}}}`)
		default:
			handler.ServeHTTP(w, r)
		}
	}))
	config.Engine, config.SpaceType = engine, space
	store, err := NewStore(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func writeContractJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	if err := jsonv2.MarshalWrite(w, value); err != nil {
		t.Error(err)
	}
}
