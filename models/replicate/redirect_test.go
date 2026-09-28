package replicate

import (
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

type downloadTransport struct {
	redirects     map[string]string
	requests      []string
	authorization []string
}

func (d *downloadTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	d.requests = append(d.requests, request.URL.String())
	d.authorization = append(d.authorization, request.Header.Get("Authorization"))
	header := http.Header{"Content-Type": []string{"image/png"}}
	status := http.StatusOK
	if target, ok := d.redirects[request.URL.String()]; ok {
		status = http.StatusFound
		header.Set("Location", target)
	}
	return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader("image")), Request: request}, nil
}

func TestOutputDownloadChecksEveryOrigin(t *testing.T) {
	for _, test := range []struct {
		name, base, start       string
		redirects               map[string]string
		requests, authorization []string
		wantError               bool
	}{
		{name: "direct outside", start: "https://outside.invalid/image", wantError: true},
		{name: "redirect outside", start: "https://api.replicate.com/start", redirects: map[string]string{"https://api.replicate.com/start": "https://outside.invalid/image"}, requests: []string{"https://api.replicate.com/start"}, authorization: []string{"Bearer test-key"}, wantError: true},
		{name: "same host downgrade", start: "http://api.replicate.com/image", wantError: true},
		{name: "same host other port", start: "https://api.replicate.com:8443/image", wantError: true},
		{name: "redirect other port", start: "https://api.replicate.com/start", redirects: map[string]string{"https://api.replicate.com/start": "https://api.replicate.com:8443/image"}, requests: []string{"https://api.replicate.com/start"}, authorization: []string{"Bearer test-key"}, wantError: true},
		{name: "delivery", start: "https://a.replicate.delivery/image", requests: []string{"https://a.replicate.delivery/image"}, authorization: []string{""}},
		{name: "delivery other port", start: "https://a.replicate.delivery:8443/image", wantError: true},
		{name: "lookalike host", start: "https://replicate.delivery.outside.invalid/image", wantError: true},
		{name: "userinfo", start: "https://user:password@a.replicate.delivery/image", wantError: true},
		{name: "same origin redirect", start: "https://api.replicate.com/start", redirects: map[string]string{"https://api.replicate.com/start": "https://api.replicate.com/image"}, requests: []string{"https://api.replicate.com/start", "https://api.replicate.com/image"}, authorization: []string{"Bearer test-key", "Bearer test-key"}},
		{name: "delivery redirect strips token", start: "https://api.replicate.com/start", redirects: map[string]string{"https://api.replicate.com/start": "https://a.replicate.delivery/image"}, requests: []string{"https://api.replicate.com/start", "https://a.replicate.delivery/image"}, authorization: []string{"Bearer test-key", ""}},
		{name: "delivery subdomain strips token", start: "https://replicate.delivery/start", redirects: map[string]string{"https://replicate.delivery/start": "https://a.replicate.delivery/image"}, requests: []string{"https://replicate.delivery/start", "https://a.replicate.delivery/image"}, authorization: []string{"", ""}},
		{name: "custom endpoint", base: "http://localhost:9123/v1", start: "http://localhost:9123/image", requests: []string{"http://localhost:9123/image"}, authorization: []string{"Bearer test-key"}},
		{name: "custom different port", base: "http://localhost:9123/v1", start: "http://localhost:9124/image", wantError: true},
		{name: "HTTPS redirect into HTTP custom endpoint", base: "http://localhost:9123/v1", start: "https://a.replicate.delivery/start", redirects: map[string]string{"https://a.replicate.delivery/start": "http://localhost:9123/image"}, requests: []string{"https://a.replicate.delivery/start"}, authorization: []string{""}, wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			transport := &downloadTransport{redirects: test.redirects}
			adapter, err := newAPI(apiConfig{APIKey: "test-key", BaseURL: test.base, HTTPClient: &http.Client{Transport: transport}})
			if err != nil {
				t.Fatal(err)
			}
			data, _, err := adapter.downloadOutput(t.Context(), test.start)
			if (err != nil) != test.wantError {
				t.Fatalf("data=%q error=%v", data, err)
			}
			if !reflect.DeepEqual(transport.requests, test.requests) || !reflect.DeepEqual(transport.authorization, test.authorization) {
				t.Fatalf("requests=%v authorization=%v", transport.requests, transport.authorization)
			}
		})
	}
}

func TestOutputDownloadRetainsBorrowedClientPolicy(t *testing.T) {
	policyErr := errors.New("host redirect policy")
	calls := 0
	source := &http.Client{Transport: &downloadTransport{redirects: map[string]string{"https://api.replicate.com/start": "https://a.replicate.delivery/image"}}, CheckRedirect: func(request *http.Request, via []*http.Request) error { calls++; return policyErr }}
	adapter, err := newAPI(apiConfig{APIKey: "test-key", HTTPClient: source})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = adapter.downloadOutput(t.Context(), "https://api.replicate.com/start"); !errors.Is(err, policyErr) {
		t.Fatalf("error=%v", err)
	}
	if calls != 1 {
		t.Fatalf("policy calls=%d", calls)
	}
	source.Transport = &downloadTransport{redirects: map[string]string{"https://api.replicate.com/start": "https://outside.invalid/image"}}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://api.replicate.com/start", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = source.Do(request); !errors.Is(err, policyErr) {
		t.Fatalf("borrowed client policy changed: %v", err)
	}
	if calls != 2 {
		t.Fatalf("borrowed policy calls=%d", calls)
	}
}

func TestConstructionDoesNotMutateBorrowedHTTPClient(t *testing.T) {
	source := &http.Client{}
	if _, err := newAPI(apiConfig{APIKey: "test-key", HTTPClient: source}); err != nil {
		t.Fatal(err)
	}
	if source.Transport != nil || source.CheckRedirect != nil {
		t.Fatal("borrowed client was mutated")
	}
}
