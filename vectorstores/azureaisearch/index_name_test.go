package azureaisearch

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestInvalidIndexNamePrecedesSchemaIO(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		fmt.Fprint(writer, nativeSchema("cosine"))
	}))
	t.Cleanup(server.Close)
	for _, name := range []string{"", ".", "..", "a", "_a", "-a", "Documents", "a/b", "a.b", "a b", "a--b", "a__b", "文档", "a\xff", strings.Repeat("a", 129)} {
		t.Run(fmt.Sprintf("%q", name), func(t *testing.T) {
			config := StoreConfig{Endpoint: server.URL, IndexName: name, HTTPClient: server.Client(), Space: testSpace}
			if err := config.Validate(); err == nil {
				t.Error("invalid native index name passed configuration validation")
			}
			before := requests.Load()
			store, err := NewStore(t.Context(), config)
			if err == nil || store != nil {
				t.Fatalf("invalid native index name constructed a store: %v", err)
			}
			if requests.Load() != before {
				t.Fatal("invalid native index name reached schema HTTP boundary")
			}
		})
	}
}

func TestIndexNamePreservesNativeBoundaries(t *testing.T) {
	for _, name := range []string{"a0", "01", "a-b_c", "a-", "a_", strings.Repeat("a", 128)} {
		t.Run(name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				requests.Add(1)
				if request.Method != http.MethodGet || request.URL.Path != "/indexes/"+name {
					t.Errorf("native index binding request = %s %s", request.Method, request.URL.Path)
				}
				fmt.Fprint(writer, nativeSchema("cosine"))
			}))
			t.Cleanup(server.Close)
			store, err := NewStore(t.Context(), StoreConfig{Endpoint: server.URL, IndexName: name, HTTPClient: server.Client(), Space: testSpace})
			if err != nil || store == nil || requests.Load() != 1 {
				t.Fatalf("valid native index binding = %v, requests = %d", err, requests.Load())
			}
		})
	}
}
