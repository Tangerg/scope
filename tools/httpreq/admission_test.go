package httpreq_test

import (
	"errors"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
	"github.com/Tangerg/scope/tools/httpreq"
)

type countingTransport struct {
	calls    *atomic.Int32
	redirect string
}

func (c countingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	c.calls.Add(1)
	if c.redirect != "" {
		return &http.Response{
			StatusCode: http.StatusFound, Header: http.Header{"Location": {c.redirect}}, Body: http.NoBody, Request: request,
		}, nil
	}
	return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody, Request: request}, nil
}

func callHTTPTool(t *testing.T, transport countingTransport, arguments string) error {
	t.Helper()
	client, err := httpreq.NewClient(httpreq.ClientConfig{
		AllowedHosts: []string{"allowed.example"}, HTTPClient: &http.Client{Transport: transport},
	})
	if err != nil {
		t.Fatal(err)
	}
	executable, err := httpreq.NewTool(client)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := tool.Bind(executable)
	if err != nil {
		t.Fatal(err)
	}
	invocation, err := binding.Contract().Prepare(chat.ToolCall{ID: "call-1", Name: "http_request", Arguments: arguments})
	if err != nil {
		t.Fatal(err)
	}
	_, err = binding.Call(t.Context(), invocation)
	return err
}

func TestToolReportsUnsentRequestsAsDefiniteFailures(t *testing.T) {
	for _, tc := range []struct {
		name      string
		arguments string
		kind      tool.FailureKind
		sentinel  error
	}{
		{"blocked host", `{"url":"https://blocked.example/"}`, tool.FailureKindRejected, httpreq.ErrHostNotAllowed},
		{"blocked method", `{"url":"https://allowed.example/","method":"POST","body":"payload"}`, tool.FailureKindRejected, httpreq.ErrMethodNotAllowed},
		{"invalid url", `{"url":"ftp://allowed.example/"}`, tool.FailureKindFailed, httpreq.ErrInvalidURL},
		{"header named twice", `{"url":"https://allowed.example/","headers":{"Authorization":"first","authorization":"second"}}`, tool.FailureKindFailed, httpreq.ErrDuplicateHeader},
		{"GET body", `{"url":"https://allowed.example/_search","body":"{\"query\":\"requested\"}"}`, tool.FailureKindFailed, httpreq.ErrBodyNotAllowed},
		{"HEAD body", `{"url":"https://allowed.example/","method":"HEAD","body":"payload"}`, tool.FailureKindFailed, httpreq.ErrBodyNotAllowed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			err := callHTTPTool(t, countingTransport{calls: &calls}, tc.arguments)
			failure, ok := errors.AsType[*tool.Failure](err)
			if !ok || failure.Kind() != tc.kind || !errors.Is(failure.Cause(), tc.sentinel) {
				t.Fatalf("unsent request error = %v, want %s failure caused by %v", err, tc.kind, tc.sentinel)
			}
			if text, ok := failure.Output().Text(); !ok || text != failure.Cause().Error() {
				t.Fatalf("failure output = %q, want %q", text, failure.Cause().Error())
			}
			if calls.Load() != 0 {
				t.Fatalf("network calls = %d, want 0", calls.Load())
			}
		})
	}
}

func TestToolKeepsRejectedRedirectsUnknown(t *testing.T) {
	var calls atomic.Int32
	err := callHTTPTool(t, countingTransport{calls: &calls, redirect: "https://blocked.example/"}, `{"url":"https://allowed.example/"}`)
	if !errors.Is(err, httpreq.ErrHostNotAllowed) || calls.Load() != 1 {
		t.Fatalf("redirect error = %v after %d calls", err, calls.Load())
	}
	if failure, ok := errors.AsType[*tool.Failure](err); ok {
		t.Fatalf("a request that was sent became a definite failure: %v", failure)
	}
}

func TestDefaultHeadersNameEachFieldOnce(t *testing.T) {
	_, err := httpreq.NewClient(httpreq.ClientConfig{
		AllowedHosts: []string{"allowed.example"}, DefaultHeaders: map[string]string{"X-Token": "first", "x-token": "second"},
	})
	if !errors.Is(err, httpreq.ErrInvalidClientConfig) || !errors.Is(err, httpreq.ErrDuplicateHeader) {
		t.Fatalf("NewClient error = %v, want a duplicate header configuration error", err)
	}
}
