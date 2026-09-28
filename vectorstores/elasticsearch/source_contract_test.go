package elasticsearch

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/elastic/go-elasticsearch/v8"
)

func TestStoredSourcePreservesRequiredFields(t *testing.T) {
	for _, sample := range []struct {
		name      string
		source    storedSource
		wantError bool
	}{
		{"default", storedSource{}, false},
		{"disabled", storedSource{Enabled: new(false)}, true},
		{"pruned metadata", storedSource{Excludes: []string{"metadata.author"}}, true},
		{"unrelated exclusion", storedSource{Excludes: []string{"embedding"}}, false},
		{"wildcard exclusion", storedSource{Excludes: []string{"meta*"}}, true},
		{"complete inclusion", storedSource{Includes: []string{"metadata", "content"}}, false},
		{"partial inclusion", storedSource{Includes: []string{"metadata.author", "content"}}, true},
		{"reconstructed", storedSource{Mode: "synthetic"}, true},
	} {
		t.Run(sample.name, func(t *testing.T) {
			err := sample.source.validate("metadata", "content")
			if errors.Is(err, ErrIncompatibleIndex) != sample.wantError {
				t.Fatalf("validate() = %v; want incompatible=%v", err, sample.wantError)
			}
		})
	}
}

func TestInitializeValidatesActualCreatedAndExistingIndex(t *testing.T) {
	for _, fresh := range []bool{false, true} {
		for _, source := range []string{`{}`, `{"enabled":false}`} {
			t.Run(fmt.Sprintf("fresh=%v/source=%s", fresh, source), func(t *testing.T) {
				created := false
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					w.Header().Set("X-Elastic-Product", "Elasticsearch")
					switch {
					case r.Method == http.MethodHead:
						if fresh {
							w.WriteHeader(http.StatusNotFound)
						}
					case r.Method == http.MethodPut:
						created = true
						_, _ = io.WriteString(w, `{"acknowledged":true,"shards_acknowledged":true,"index":"documents"}`)
					case strings.Contains(r.URL.Path, "_mapping"):
						_, _ = fmt.Fprintf(w, `{"documents":{"mappings":{"_source":%s,"properties":{"embedding":{"type":"dense_vector","dims":2,"similarity":"cosine"}}}}}`, source)
					case strings.Contains(r.URL.Path, "_settings"):
						_, _ = io.WriteString(w, `{"documents":{"settings":{},"defaults":{"index.mapping.source.mode":"stored"}}}`)
					default:
						t.Errorf("unexpected request %s %s", r.Method, r.URL)
						w.WriteHeader(500)
					}
				}))
				t.Cleanup(server.Close)
				client, err := elasticsearch.NewClient(elasticsearch.Config{Addresses: []string{server.URL}, Transport: server.Client().Transport})
				if err != nil {
					t.Fatal(err)
				}
				store := &Store{client: client, indexName: "documents", metadataField: "metadata", contentField: "content", embeddingField: "embedding", dimensions: 2, similarity: SimilarityCosine}
				err = store.initialize(t.Context(), fresh)
				if errors.Is(err, ErrIncompatibleIndex) != (source != `{}`) {
					t.Fatalf("initialize = %v", err)
				}
				if source == `{}` && err != nil {
					t.Fatal(err)
				}
				if created != fresh {
					t.Fatalf("created=%v, want %v", created, fresh)
				}
			})
		}
	}
}

func TestAliasBindingCannotDiscardFilterAndRouting(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Elastic-Product", "Elasticsearch")
		if r.Method != http.MethodHead {
			_, _ = io.WriteString(w, `{"physical":{"mappings":{"properties":{"embedding":{"type":"dense_vector","dims":2,"similarity":"cosine"}}}}}`)
		}
	}))
	t.Cleanup(server.Close)
	client, err := elasticsearch.NewClient(elasticsearch.Config{Addresses: []string{server.URL}, Transport: server.Client().Transport})
	if err != nil {
		t.Fatal(err)
	}
	store := &Store{client: client, indexName: "tenant-alias", metadataField: "metadata", contentField: "content", embeddingField: "embedding", dimensions: 2, similarity: SimilarityCosine}
	if err := store.initialize(t.Context(), false); !errors.Is(err, errors.ErrUnsupported) {
		t.Fatalf("alias accepted: %v", err)
	}
	if store.indexName != "tenant-alias" {
		t.Fatalf("alias silently rebound to %s", store.indexName)
	}
}
