package weaviate

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"google.golang.org/grpc/metadata"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/vectorstore"
)

func TestRESTAuthorityCannotFollowRedirects(t *testing.T) {
	const key = "test-credential"
	const id = "00000000-0000-4000-8000-000000000001"
	for _, stage := range []string{"meta", "schema", "delete"} {
		for _, otherHost := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/other_host=%v", stage, otherHost), func(t *testing.T) {
				var escaped atomic.Int32
				target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					escaped.Add(1)
					if r.Header.Get("Authorization") != "" {
						t.Error("credential reached untrusted redirect destination")
					}
					w.WriteHeader(204)
				}))
				defer target.Close()
				loc := target.URL
				if otherHost {
					loc = strings.Replace(loc, "127.0.0.1", "localhost", 1)
				}
				var challenged atomic.Bool
				origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Header.Get("Authorization") != "Bearer "+key {
						t.Error("origin did not receive its own credential")
					}
					switch {
					case stage == "meta" && r.URL.Path == "/v1/meta", stage == "schema" && r.URL.Path == "/v1/schema/Documents", stage == "delete" && r.Method == http.MethodDelete:
						challenged.Store(true)
						http.Redirect(w, r, loc+"/replaced", http.StatusTemporaryRedirect)
					case r.URL.Path == "/v1/meta":
						fmt.Fprint(w, `{"version":"1.40.3","grpcMaxMessageSize":16777216}`)
					case r.URL.Path == "/v1/schema/Documents":
						fmt.Fprintf(w, `{"class":"Documents","description":%q,"vectorConfig":{"embedding":{"vectorizer":{"none":{}},"vectorIndexType":"hnsw","vectorIndexConfig":{"distance":"cosine"}}},"properties":[{"name":"content","dataType":["text"],"tokenization":"word"},{"name":"metadata","dataType":["text"],"tokenization":"field"},{"name":"metadataTerms","dataType":["text[]"],"tokenization":"field","indexFilterable":true,"indexSearchable":false}]}`, fixtureSpace.StorageKey())
					default:
						t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
						w.WriteHeader(404)
					}
				}))
				defer origin.Close()
				c, err := NewClient(t.Context(), ClientConfig{Endpoint: origin.URL, GRPCAddress: "127.0.0.1:1", APIKey: key})
				if c != nil {
					defer c.Close()
				}
				if err == nil {
					var s *Store
					s, err = NewStore(t.Context(), StoreConfig{Client: c, Space: fixtureSpace, Collection: "Documents"})
					if err == nil {
						err = s.DeleteIDs(t.Context(), []string{id})
					}
				}
				if !challenged.Load() || err == nil || escaped.Load() != 0 {
					t.Fatalf("challenged=%t err=%v foreign requests=%d", challenged.Load(), err, escaped.Load())
				}
			})
		}
	}
}

func TestGRPCAuthorityDoesNotInheritOutgoingCredentials(t *testing.T) {
	f := &nativeFixture{apiKey: "only-this-grant"}
	s := newNativeStore(t, f)
	ctx := metadata.NewOutgoingContext(t.Context(), metadata.Pairs("authorization", "Bearer unrelated-credential"))
	if err := s.Upsert(ctx, fixtureBatch([]*document.Document{{ID: "00000000-0000-4000-8000-000000000001", Text: "hi"}})); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Query(ctx, &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("q")}); err != nil {
		t.Fatal(err)
	}
}

func TestClientRejectsInvalidOriginAndResourceQualifiersBeforeIO(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(server.Close)
	for _, endpoint := range []string{
		"http://:80", "http://[]:80", "file:///tmp/local", "https://user@service.example",
		server.URL + "/base", server.URL + "?scope=a", server.URL + "?", server.URL + "#fragment",
	} {
		t.Run(endpoint, func(t *testing.T) {
			client, err := NewClient(t.Context(), ClientConfig{Endpoint: endpoint, GRPCAddress: "127.0.0.1:1"})
			if client != nil {
				_ = client.Close()
			}
			if client != nil || err == nil || requests.Load() != 0 {
				t.Fatalf("invalid endpoint reached an open client or I/O: %v, %v, requests=%d", client, err, requests.Load())
			}
		})
	}
}
