package bedrockkb_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/httpclient"
	"github.com/Tangerg/scope/rag"
	"github.com/Tangerg/scope/retrievers/bedrockkb"
)

func TestRetrieveHTTPFailurePreservesSafeEvidence(t *testing.T) {
	const body = "private-response-content"
	for _, test := range []struct{ name, header, requestID, wantID string }{
		{"safe identity", "X-Request-Id", "request-123", "request-123"},
		{"unsafe identity", "X-Request-Id", "private request identity", ""},
		{"native identity", "X-Amzn-Requestid", "aws-request-123", "aws-request-123"},
		{"unsafe native identity", "X-Amzn-Requestid", "private aws identity", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {

				writer.Header().Set(test.header, test.requestID)
				writer.WriteHeader(http.StatusTooManyRequests)
				_, _ = writer.Write([]byte(body))
			}))
			t.Cleanup(server.Close)
			retriever, err := bedrockkb.New(t.Context(), bedrockkb.Config{Endpoint: server.URL, HTTPClient: server.Client(), KnowledgeBaseID: "KB12345678"})
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
