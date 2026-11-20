package protocol

import (
	"errors"
	"net/http"
	"testing"

	"github.com/Tangerg/scope/core/httpclient"
)

func TestResponseErrorKeepsTheCauseBesideTransportEvidence(t *testing.T) {
	cause := errors.New("provider failure")
	err := newResponseError(cause, http.StatusTooManyRequests, http.Header{"Retry-After": {"17"}})
	evidence, ok := errors.AsType[*httpclient.ResponseError](err)
	if !errors.Is(err, cause) || !ok || evidence.HTTPStatus() != http.StatusTooManyRequests || evidence.HTTPHeader().Get("Retry-After") != "17" {
		t.Fatalf("error = %v", err)
	}
}
