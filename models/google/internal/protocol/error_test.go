package protocol

import (
	"errors"
	"net/http"
	"testing"
)

func TestResponseErrorSnapshotsHeaders(t *testing.T) {
	cause := errors.New("provider failure")
	header := http.Header{"Retry-After": {"17"}, "X-Request-Id": {"request-123"}}
	err := newResponseError(cause, http.StatusTooManyRequests, header)
	header["Retry-After"][0] = "0"
	delete(header, "X-Request-Id")
	if !errors.Is(err, cause) || err.HTTPStatus() != http.StatusTooManyRequests || err.HTTPHeader().Get("Retry-After") != "17" || err.HTTPHeader().Get("X-Request-Id") != "request-123" {
		t.Fatalf("cause=%v status=%d headers=%v", err, err.HTTPStatus(), err.HTTPHeader())
	}
}
