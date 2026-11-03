package httpclient_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/httpclient"
)

func TestResponseErrorRetainsOnlyBoundedRequestIdentity(t *testing.T) {
	for _, test := range []struct {
		name, supplied, retained string
	}{
		{name: "opaque identifier", supplied: "req_01.AB-23:45/67", retained: "req_01.AB-23:45/67"},
		{name: "absent"},
		{name: "header injection", supplied: "request\r\nAuthorization: private"},
		{name: "text body", supplied: "private prompt with spaces"},
		{name: "non ASCII", supplied: "request-秘密"},
		{name: "maximum length", supplied: strings.Repeat("x", 128), retained: strings.Repeat("x", 128)},
		{name: "oversized", supplied: strings.Repeat("x", 129)},
	} {
		t.Run(test.name, func(t *testing.T) {
			failure := httpclient.NewResponseError(http.StatusTooManyRequests, test.supplied)
			if failure.HTTPStatus() != http.StatusTooManyRequests || failure.RequestID() != test.retained {
				t.Fatalf("status=%d request_id=%q", failure.HTTPStatus(), failure.RequestID())
			}
			want := "http response: status 429"
			if test.retained != "" {
				want += " (request_id=" + test.retained + ")"
			}
			if failure.Error() != want {
				t.Fatalf("diagnostic=%q, want %q", failure.Error(), want)
			}
		})
	}
}
