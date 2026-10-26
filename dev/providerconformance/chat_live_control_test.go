package providerconformance_test

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tangerg/scope/core/httpclient"
)

const (
	maxLiveResponseBytes = 2 << 20
	liveRequestTimeout   = 30 * time.Second
)

var errLiveBudgetExhausted = errors.New("live chat: physical request budget exhausted")

// The budget counts physical requests, including SDK retries. Neither redirects
// nor an SDK-selected alternate origin may spend a dedicated test credential.
type liveTransport struct {
	base      http.RoundTripper
	origin    httpclient.Origin
	remaining atomic.Int64
}

func (l *liveTransport) admit(request *http.Request) error {
	if err := l.origin.ValidateRequest(request); err != nil {
		return fmt.Errorf("live chat: %w", err)
	}
	if l.remaining.Add(-1) < 0 {
		return errLiveBudgetExhausted
	}
	return nil
}

func (l *liveTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if err := l.admit(request); err != nil {
		if request != nil && request.Body != nil {
			err = errors.Join(err, request.Body.Close())
		}
		return nil, err
	}
	response, err := l.base.RoundTrip(request)
	if err != nil {
		return nil, err
	}
	response.Body = http.MaxBytesReader(nil, response.Body, maxLiveResponseBytes)
	return response, nil
}

func newLiveHTTPClient(t *testing.T, baseURL string, budget int64) *http.Client {
	t.Helper()
	endpoint, err := url.Parse(baseURL)
	if err != nil {
		t.Fatal(err)
	}
	origin, err := httpclient.NewOrigin(t.Context(), endpoint)
	if err != nil {
		t.Fatal(err)
	}
	base := http.DefaultTransport.(*http.Transport).Clone()
	t.Cleanup(base.CloseIdleConnections)
	transport := &liveTransport{base: base, origin: origin}
	transport.remaining.Store(budget)
	return &http.Client{
		Transport: transport, Timeout: liveRequestTimeout,
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
	client := newLiveHTTPClient(t, server.URL, 1)
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
		_, _ = io.WriteString(writer, strings.Repeat("x", maxLiveResponseBytes+1))
	}))
	defer server.Close()
	client := newLiveHTTPClient(t, server.URL, 1)
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

type liveRequestBody struct {
	io.Reader
	closed   int
	closeErr error
}

func (l *liveRequestBody) Close() error {
	l.closed++
	return l.closeErr
}

type unexpectedLiveTransport struct {
	calls int
}

func (u *unexpectedLiveTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	u.calls++
	if request.Body != nil {
		_ = request.Body.Close()
	}
	return nil, errors.New("unexpected physical request")
}

func TestLiveTransportClosesRejectedRequests(t *testing.T) {
	const endpoint = "https://api.scope.test"
	for _, test := range []struct {
		name   string
		budget int64
		mutate func(*http.Request)
	}{
		{name: "destination", budget: 1, mutate: func(request *http.Request) {
			request.URL.Host = "other.scope.test"
		}},
		{name: "authority override", budget: 1, mutate: func(request *http.Request) {
			request.Host = "other.scope.test"
		}},
		{name: "user information", budget: 1, mutate: func(request *http.Request) {
			request.URL.User = url.User("credential")
		}},
		{name: "exhausted budget", budget: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			closeErr := errors.New("request body close failed")
			body := &liveRequestBody{Reader: strings.NewReader("request"), closeErr: closeErr}
			request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, endpoint, body)
			if err != nil {
				t.Fatal(err)
			}
			if test.mutate != nil {
				test.mutate(request)
			}
			transport := newLiveHTTPClient(t, endpoint, test.budget).Transport.(*liveTransport)
			physical := new(unexpectedLiveTransport)
			transport.base = physical
			response, err := transport.RoundTrip(request)
			if response != nil || !errors.Is(err, closeErr) || body.closed != 1 || physical.calls != 0 {
				t.Errorf("rejected request: response=%v error=%v closes=%d physical=%d", response, err, body.closed, physical.calls)
			}
			if test.mutate != nil {
				if !errors.Is(err, httpclient.ErrOriginNotAllowed) || transport.remaining.Load() != test.budget {
					t.Errorf("origin rejection: error=%v remaining=%d", err, transport.remaining.Load())
				}
			} else if !errors.Is(err, errLiveBudgetExhausted) {
				t.Errorf("budget rejection lost its cause: %v", err)
			}
		})
	}
}
