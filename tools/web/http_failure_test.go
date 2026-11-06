package web_test

import (
	"errors"
	"net/http"
	"testing"

	"github.com/Tangerg/scope/tools/web"
)

func TestProvidersPreserveHTTPFailureEvidence(t *testing.T) {
	for _, provider := range testProviders {
		t.Run(provider.name, func(t *testing.T) {
			client := newTestProvider(t, provider, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("X-Request-ID", "search-request")
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"error":"upstream limit"}`))
			})
			response, err := client.Search(t.Context(), &web.SearchRequest{Query: "scope"})
			if response != nil || err == nil {
				t.Fatalf("unexpected search result: response=%v error=%v", response, err)
			}
			var status interface{ HTTPStatus() int }
			var requestID interface{ RequestID() string }
			statusOK := errors.As(err, &status)
			requestIDOK := errors.As(err, &requestID)
			t.Logf("HTTP status typed=%t request ID typed=%t error=%v", statusOK, requestIDOK, err)
			if !statusOK || status.HTTPStatus() != http.StatusTooManyRequests || !requestIDOK || requestID.RequestID() != "search-request" {
				t.Error("provider discarded received structured HTTP failure evidence")
			}
		})
	}
}
