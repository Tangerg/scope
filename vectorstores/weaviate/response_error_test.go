package weaviate

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/httpclient"
)

func TestRESTFailuresPreserveTransportEvidence(t *testing.T) {
	const requestID = "request-429"
	for _, stage := range []string{"meta", "schema", "delete"} {
		for _, status := range []int{http.StatusTooManyRequests, http.StatusServiceUnavailable} {
			t.Run(fmt.Sprintf("%s/%d", stage, status), func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch {
					case stage == "meta" && r.URL.Path == "/v1/meta", stage == "schema" && r.URL.Path == "/v1/schema/Documents", stage == "delete" && r.Method == http.MethodDelete:
						w.Header().Set("X-Request-Id", requestID)
						w.WriteHeader(status)
						_, _ = w.Write([]byte("private upstream response"))
					case r.URL.Path == "/v1/meta":
						_, _ = w.Write([]byte(`{}`))
					case r.URL.Path == "/v1/schema/Documents":
						fmt.Fprintf(w, `{"class":"Documents","description":%q,"vectorConfig":{"embedding":{"vectorizer":{"none":{}},"vectorIndexType":"hnsw","vectorIndexConfig":{"distance":"cosine"}}},"properties":[{"name":"content","dataType":["text"],"tokenization":"word"},{"name":"metadata","dataType":["text"],"tokenization":"field"},{"name":"metadataTerms","dataType":["text[]"],"tokenization":"field","indexFilterable":true,"indexSearchable":false}]}`, fixtureSpace.StorageKey())
					default:
						t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
						w.WriteHeader(http.StatusNotFound)
					}
				}))
				t.Cleanup(server.Close)
				client, err := NewClient(t.Context(), ClientConfig{Endpoint: server.URL, GRPCAddress: "127.0.0.1:1"})
				if client != nil {
					t.Cleanup(func() {
						if closeErr := client.Close(); closeErr != nil {
							t.Error(closeErr)
						}
					})
				}
				if err == nil {
					var store *Store
					store, err = NewStore(t.Context(), StoreConfig{Client: client, Space: fixtureSpace, Collection: "Documents"})
					if err == nil {
						err = store.DeleteIDs(t.Context(), []string{"00000000-0000-4000-8000-000000000001"})
					}
				}
				response, ok := errors.AsType[*httpclient.ResponseError](err)
				if !ok || response.HTTPStatus() != status || response.RequestID() != requestID {
					t.Fatalf("REST error lost transport evidence: %T %v", err, err)
				}
				if strings.Contains(err.Error(), "private upstream response") {
					t.Fatalf("REST error exposed response body: %v", err)
				}
			})
		}
	}
}

func TestRESTDeletionAcceptsRemovedAndMissingObjects(t *testing.T) {
	for _, status := range []int{http.StatusNoContent, http.StatusNotFound} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet && r.URL.Path == "/v1/meta" {
					_, _ = w.Write([]byte(`{}`))
					return
				}
				if r.Method != http.MethodDelete || r.URL.Path != "/v1/objects/Documents/00000000-0000-4000-8000-000000000001" {
					t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
				}
				w.WriteHeader(status)
			}))
			t.Cleanup(server.Close)
			client, err := NewClient(t.Context(), ClientConfig{Endpoint: server.URL, GRPCAddress: "127.0.0.1:1"})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if closeErr := client.Close(); closeErr != nil {
					t.Error(closeErr)
				}
			})
			if err := client.delete(t.Context(), "Documents", "00000000-0000-4000-8000-000000000001"); err != nil {
				t.Fatalf("idempotent deletion failed: %v", err)
			}
		})
	}
}
