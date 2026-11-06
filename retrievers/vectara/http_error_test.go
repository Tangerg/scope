package vectara_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/httpclient"
	"github.com/Tangerg/scope/rag"
	"github.com/Tangerg/scope/retrievers/vectara"
)

func TestRetrieveHTTPFailurePreservesSafeEvidence(t *testing.T) {
	const body = "private-response-content"
	for _, test := range []struct{ name, requestID, wantID string }{
		{"safe identity", "request-123", "request-123"},
		{"unsafe identity", "private request identity", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.Method == http.MethodGet && request.URL.Path == "/v2/corpora/manuals" {
					_, _ = writer.Write([]byte(`{"key":"manuals","enabled":true}`))
					return
				}
				writer.Header().Set("X-Request-Id", test.requestID)
				writer.WriteHeader(http.StatusTooManyRequests)
				_, _ = writer.Write([]byte(body))
			}))
			t.Cleanup(server.Close)
			retriever, err := vectara.New(t.Context(), vectara.Config{Endpoint: server.URL, HTTPClient: server.Client(), CorpusKey: "manuals", APIKey: "test-key"})
			if err != nil {
				t.Fatal(err)
			}
			query, err := rag.NewQuery("query")
			if err != nil {
				t.Fatal(err)
			}
			candidates, err := retriever.Retrieve(t.Context(), query)
			failure, ok := errors.AsType[*httpclient.ResponseError](err)
			if candidates != nil || !ok || failure.HTTPStatus() != http.StatusTooManyRequests || failure.RequestID() != test.wantID {
				t.Fatalf("candidates=%v error=%v", candidates, err)
			}
			if strings.Contains(err.Error(), body) || test.wantID == "" && strings.Contains(err.Error(), test.requestID) {
				t.Fatalf("unsafe diagnostic: %v", err)
			}
		})
	}
}
