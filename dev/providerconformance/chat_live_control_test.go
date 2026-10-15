package providerconformance_test

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// The budget counts physical requests, including SDK retries. Neither redirects
// nor an SDK-selected alternate origin may spend a dedicated test credential.
type liveTransport struct {
	base      http.RoundTripper
	origin    *url.URL
	remaining atomic.Int64
}

func (l *liveTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.URL.Scheme != l.origin.Scheme || request.URL.Host != l.origin.Host || request.URL.User != nil {
		return nil, errors.New("live chat: request escaped the selected official origin")
	}
	if l.remaining.Add(-1) < 0 {
		return nil, errors.New("live chat: physical request budget exhausted")
	}
	response, err := l.base.RoundTrip(request)
	if err != nil {
		return nil, err
	}
	response.Body = http.MaxBytesReader(nil, response.Body, 2<<20)
	return response, nil
}

func newLiveHTTPClient(t *testing.T, baseURL string, budget int64) *http.Client {
	t.Helper()
	origin, err := url.Parse(baseURL)
	if err != nil {
		t.Fatal(err)
	}
	base := http.DefaultTransport.(*http.Transport).Clone()
	t.Cleanup(base.CloseIdleConnections)
	transport := &liveTransport{base: base, origin: origin}
	transport.remaining.Store(budget)
	return &http.Client{
		Transport: transport, Timeout: 30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func TestLiveTransportBoundsRequestsAndOrigin(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(writer, "ok")
	}))
	defer server.Close()
	origin, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	transport := &liveTransport{base: server.Client().Transport, origin: origin}
	transport.remaining.Store(1)
	client := &http.Client{Transport: transport}
	response, err := client.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if _, err := client.Get("https://example.invalid"); err == nil {
		t.Fatal("cross-origin request accepted")
	}
	if _, err := client.Get(server.URL); err == nil {
		t.Fatal("request budget was exceeded")
	}
	if calls.Load() != 1 {
		t.Fatalf("physical requests = %d; want 1", calls.Load())
	}
}

func TestLiveTransportRejectsOversizedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = io.WriteString(writer, strings.Repeat("x", (2<<20)+1))
	}))
	defer server.Close()
	origin, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	transport := &liveTransport{base: server.Client().Transport, origin: origin}
	transport.remaining.Store(1)
	client := &http.Client{Transport: transport}
	response, err := client.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	_, readErr := io.ReadAll(response.Body)
	var tooLarge *http.MaxBytesError
	if !errors.As(readErr, &tooLarge) {
		t.Fatalf("read error = %v; want MaxBytesError", readErr)
	}
}

func TestLiveClientDoesNotFollowRedirects(t *testing.T) {
	var reached atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		reached.Store(true)
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, target.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	client := newLiveHTTPClient(t, source.URL, 1)
	response, err := client.Get(source.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusTemporaryRedirect || reached.Load() {
		t.Fatal("live client followed a redirect")
	}
}
