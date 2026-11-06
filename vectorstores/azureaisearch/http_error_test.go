package azureaisearch

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/httpclient"
)

func TestHTTPFailureRetainsSafeEvidence(t *testing.T) {
	const body = "private-response-content"
	for _, identity := range []struct{ value, want string }{{"request-123", "request-123"}, {"private request identity", ""}} {
		t.Run(identity.value, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set("X-Request-Id", identity.value)
				writer.WriteHeader(http.StatusTooManyRequests)
				_, _ = writer.Write([]byte(body))
			}))
			t.Cleanup(server.Close)
			store, err := NewStore(t.Context(), StoreConfig{Endpoint: server.URL, HTTPClient: server.Client(), IndexName: "documents", Space: "test"})
			if store != nil {
				t.Fatal("failed construction returned a store")
			}
			failure, ok := errors.AsType[*httpclient.ResponseError](err)
			if !ok || failure.HTTPStatus() != http.StatusTooManyRequests || failure.RequestID() != identity.want {
				t.Fatalf("error=%v", err)
			}
			if strings.Contains(err.Error(), body) || identity.want == "" && strings.Contains(err.Error(), identity.value) {
				t.Fatalf("unsafe diagnostic: %v", err)
			}
		})
	}
}
