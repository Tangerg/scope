package opensearch

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Tangerg/scope/vectorstores/protocol/elastic"

	"github.com/Tangerg/scope/core/httpclient"
)

type responseClient struct {
	status    int
	requestID string
	failure   error
}

func (r responseClient) Stream(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: r.status, Header: http.Header{"X-Request-Id": []string{r.requestID}}, Body: io.NopCloser(strings.NewReader("private-response-content"))}, r.failure
}

func TestHTTPFailureRetainsSafeEvidenceAndIndexClassification(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusNotFound} {
		for _, identity := range []struct{ value, want string }{{"request-123", "request-123"}, {"private request identity", ""}} {
			store, err := NewStore(t.Context(), StoreConfig{Client: responseClient{status: status, requestID: identity.value}, Space: "test"})
			failure, ok := errors.AsType[*httpclient.ResponseError](err)
			if store != nil || !ok || failure.HTTPStatus() != status || failure.RequestID() != identity.want {
				t.Fatalf("store=%v error=%v", store, err)
			}
			if errors.Is(err, elastic.ErrIndexMissing) != (status == http.StatusNotFound) {
				t.Fatalf("index classification=%v", err)
			}
			if strings.Contains(err.Error(), "private-response-content") || identity.want == "" && strings.Contains(err.Error(), identity.value) {
				t.Fatalf("unsafe diagnostic: %v", err)
			}
		}
	}
}

func TestNativeClientFailureRemainsTheCause(t *testing.T) {
	cause := errors.New("native transport failure")
	_, err := NewStore(t.Context(), StoreConfig{Client: responseClient{failure: cause}, Space: "test"})
	if !errors.Is(err, cause) {
		t.Fatalf("native failure lost: %v", err)
	}
	if _, ok := errors.AsType[*httpclient.ResponseError](err); ok {
		t.Fatalf("transport failure replaced by response classification: %v", err)
	}
}
