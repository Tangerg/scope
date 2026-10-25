package replicate

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/httpclient"
)

type outputRequestBody struct {
	io.Reader
	closed     int
	closeError error
}

func (o *outputRequestBody) Close() error {
	o.closed++
	return o.closeError
}

type outputTerminalTransport struct {
	requests []*http.Request
	closed   int
}

func (o *outputTerminalTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.Body != nil {
		_ = request.Body.Close()
	}
	o.requests = append(o.requests, request)
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: http.NoBody, Request: request}, nil
}

func (o *outputTerminalTransport) CloseIdleConnections() {
	o.closed++
}

func TestOutputTransportRejectsActualAuthorityAndClosesBody(t *testing.T) {
	for _, change := range []string{"url", "host"} {
		t.Run(change, func(t *testing.T) {
			next := new(outputTerminalTransport)
			adapter, err := newAPI(t.Context(), apiConfig{APIKey: "test-key", BaseURL: "https://api.example/v1", HTTPClient: &http.Client{Transport: next}})
			if err != nil {
				t.Fatal(err)
			}
			request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://api.example/asset", nil)
			if err != nil {
				t.Fatal(err)
			}
			if change == "url" {
				request.URL.Host = "other.example"
			} else {
				request.Host = "other.example"
			}
			closeError := errors.New("close failed")
			body := &outputRequestBody{Reader: strings.NewReader("private"), closeError: closeError}
			request.Body = body
			response, err := adapter.download.GetClient().Do(request)
			if response != nil || len(next.requests) != 0 || body.closed != 1 || !errors.Is(err, httpclient.ErrOriginNotAllowed) || !errors.Is(err, closeError) {
				t.Fatalf("response=%v requests=%d closes=%d error=%v", response, len(next.requests), body.closed, err)
			}
		})
	}
}

func TestOutputTransportOwnsCredentialIsolationAndRetainsHostCleanup(t *testing.T) {
	next := new(outputTerminalTransport)
	host := &http.Client{Transport: next}
	adapter, err := newAPI(t.Context(), apiConfig{APIKey: "test-key", HTTPClient: host})
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://a.replicate.delivery/asset", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, header := range []string{headerAuthorization, headerProxyAuthorization, headerCookie} {
		request.Header.Set(header, "private")
	}
	client := adapter.download.GetClient()
	response, err := client.Do(request)
	if err != nil || response == nil || len(next.requests) != 1 {
		t.Fatalf("response=%v requests=%d error=%v", response, len(next.requests), err)
	}
	_ = response.Body.Close()
	for _, header := range []string{headerAuthorization, headerProxyAuthorization, headerCookie} {
		if request.Header.Get(header) != "private" || next.requests[0].Header.Get(header) != "" {
			t.Fatalf("credential policy changed the original request or leaked %s", header)
		}
	}
	if next.closed != 0 || host.Transport != next || host.CheckRedirect != nil {
		t.Fatal("download client acquired ownership of the borrowed client")
	}
	client.CloseIdleConnections()
	if next.closed != 1 {
		t.Fatal("download wrapper hid explicitly requested connection cleanup")
	}
}
