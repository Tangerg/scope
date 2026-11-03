package providerconformance_test

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/httpclient"
	"github.com/Tangerg/scope/core/invocation"
	"github.com/Tangerg/scope/models/voyage"
)

type failureResponseTransport struct{ calls int }

func (f *failureResponseTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	f.calls++
	if request.Body != nil {
		_ = request.Body.Close()
	}
	return &http.Response{
		StatusCode: http.StatusUnprocessableEntity,
		Header:     http.Header{"Content-Type": {"application/json"}, "Request-Id": {"request-123"}},
		Body:       io.NopCloser(strings.NewReader(`{"message":"private prompt sentinel","detail":"private prompt sentinel","error":{"message":"private prompt sentinel"}}`)),
		Request:    request,
	}, nil
}

func TestRESTFailuresExposeHTTPFactsWithoutRemoteBody(t *testing.T) {
	covered := 0
	for _, provider := range modelOriginCalls() {
		if !provider.safeHTTPError {
			continue
		}
		covered++
		t.Run(provider.name, func(t *testing.T) {
			transport := new(failureResponseTransport)
			err := provider.call(t.Context(), "https://api.scope.test", &http.Client{Transport: transport})
			responseError, found := errors.AsType[*httpclient.ResponseError](err)
			if !found || responseError.HTTPStatus() != http.StatusUnprocessableEntity || transport.calls != 1 {
				t.Fatalf("HTTP error=%v calls=%d", err, transport.calls)
			}
			if strings.Contains(err.Error(), "private prompt sentinel") {
				t.Fatalf("remote body escaped into diagnostics: %v", err)
			}
			if invocation.ClassifyFailure(err) != invocation.FailureUnknown {
				t.Fatalf("HTTP status became execution evidence: %v", err)
			}
		})
	}
	if covered != 15 {
		t.Fatalf("REST family coverage=%d, want 15", covered)
	}
}

type oversizedResponseBody struct {
	remaining, read int
	closed          bool
}

func (o *oversizedResponseBody) Read(buffer []byte) (int, error) {
	if o.remaining == 0 {
		return 0, io.EOF
	}
	size := min(len(buffer), o.remaining)
	for index := range buffer[:size] {
		buffer[index] = ' '
	}
	o.remaining -= size
	o.read += size
	return size, nil
}

func (o *oversizedResponseBody) Close() error { o.closed = true; return nil }

type oversizedResponseTransport struct{ body *oversizedResponseBody }

func (o *oversizedResponseTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.Body != nil {
		_ = request.Body.Close()
	}
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: o.body, ContentLength: -1, Request: request}, nil
}

func TestRESTResponseBudgetStopsAnOversizedBodyBeforeDecoding(t *testing.T) {
	const excessBytes = 1 << 20
	body := &oversizedResponseBody{remaining: httpclient.DefaultMaxJSONResponseBytes + excessBytes}
	model, err := voyage.NewEmbeddingModel(t.Context(), voyage.EmbeddingModelConfig{APIKey: "key", BaseURL: "https://api.scope.test", HTTPClient: &http.Client{Transport: &oversizedResponseTransport{body: body}}, DefaultOptions: embedding.Options{Model: "model"}})
	if err != nil {
		t.Fatal(err)
	}
	response, err := model.Call(t.Context(), &embedding.Request{Texts: []string{"text"}})
	if err == nil || response != nil || !body.closed || body.remaining == 0 || body.read <= httpclient.DefaultMaxJSONResponseBytes {
		t.Fatalf("response=%v error=%v bytes=%d remaining=%d closed=%v", response, err, body.read, body.remaining, body.closed)
	}
	if invocation.ClassifyFailure(err) != invocation.FailureUnknown {
		t.Fatalf("oversized remote response lost dispatch uncertainty: %v", err)
	}
}
