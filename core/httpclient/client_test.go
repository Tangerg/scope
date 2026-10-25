package httpclient_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Tangerg/scope/core/httpclient"
)

type recordingTransport struct {
	roundTrip func(*http.Request) (*http.Response, error)
	closed    int
}

type trackedBody struct {
	io.Reader
	closed     int
	closeError error
}

func (t *trackedBody) Close() error {
	t.closed++
	return t.closeError
}

func (r *recordingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return r.roundTrip(request)
}

func (r *recordingTransport) CloseIdleConnections() { r.closed++ }

func reply(request *http.Request, status int, location string) *http.Response {
	return &http.Response{
		StatusCode: status, Header: http.Header{"Location": {location}},
		Body: io.NopCloser(strings.NewReader("")), Request: request,
	}
}

func TestRedirectOriginUsesSchemeHostAndEffectivePort(t *testing.T) {
	for _, status := range []int{http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		for _, test := range []struct {
			location string
			allowed  bool
		}{
			{location: "/next", allowed: true},
			{location: "https://API.example:443/next", allowed: true},
			{location: "https://other.example/next"},
			{location: "https://child.api.example/next"},
			{location: "https://api.example:444/next"},
			{location: "http://api.example/next"},
		} {
			t.Run(fmt.Sprintf("%d/%s", status, test.location), func(t *testing.T) {
				calls := 0
				transport := &recordingTransport{roundTrip: func(request *http.Request) (*http.Response, error) {
					calls++
					if request.Header.Get("X-API-Key") != "credential" {
						t.Fatal("authorized request lost its credential")
					}
					if calls == 1 {
						return reply(request, status, test.location), nil
					}
					return reply(request, http.StatusOK, ""), nil
				}}
				client, err := httpclient.New(t.Context(), httpclient.Config{
					BaseURL: "https://api.example/base", HTTPClient: &http.Client{Transport: transport},
				})
				if err != nil {
					t.Fatal(err)
				}
				request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "https://api.example/first", strings.NewReader("private request"))
				if err != nil {
					t.Fatal(err)
				}
				request.Header.Set("X-API-Key", "credential")
				response, err := client.Do(request)
				if response != nil {
					_ = response.Body.Close()
				}
				if test.allowed {
					if err != nil || calls != 2 {
						t.Fatalf("same-origin request: calls=%d error=%v", calls, err)
					}
				} else if !errors.Is(err, httpclient.ErrOriginNotAllowed) || calls != 1 {
					t.Fatalf("unauthorized destination: calls=%d error=%v", calls, err)
				}
			})
		}
	}
}

func TestHostRedirectPolicyCannotChangeTheAuthorizedDestination(t *testing.T) {
	denied := errors.New("host rejected redirect")
	for _, change := range []string{"deny", "url", "host"} {
		t.Run(change, func(t *testing.T) {
			calls, policyCalls := 0, 0
			transport := &recordingTransport{roundTrip: func(request *http.Request) (*http.Response, error) {
				calls++
				return reply(request, http.StatusTemporaryRedirect, "/next"), nil
			}}
			client, err := httpclient.New(t.Context(), httpclient.Config{
				BaseURL: "https://api.example",
				HTTPClient: &http.Client{Transport: transport, CheckRedirect: func(request *http.Request, _ []*http.Request) error {
					policyCalls++
					switch change {
					case "deny":
						return denied
					case "url":
						request.URL, _ = url.Parse("https://other.example/next")
						request.Host = "api.example"
					case "host":
						request.Host = "other.example"
					}
					return nil
				}},
			})
			if err != nil {
				t.Fatal(err)
			}
			response, err := client.Get("https://api.example/first")
			if response != nil {
				_ = response.Body.Close()
			}
			want := httpclient.ErrOriginNotAllowed
			if change == "deny" {
				want = denied
			}
			if !errors.Is(err, want) || calls != 1 || policyCalls != 1 {
				t.Fatalf("policy composition: calls=%d policy=%d error=%v", calls, policyCalls, err)
			}
		})
	}
}

func TestOriginGuardAppliesBeforeTheFirstTransportCall(t *testing.T) {
	for _, change := range []string{"url", "host"} {
		t.Run(change, func(t *testing.T) {
			calls := 0
			client, err := httpclient.New(t.Context(), httpclient.Config{
				BaseURL: "https://api.example",
				HTTPClient: &http.Client{Transport: &recordingTransport{roundTrip: func(request *http.Request) (*http.Response, error) {
					calls++
					return reply(request, http.StatusOK, ""), nil
				}}},
			})
			if err != nil {
				t.Fatal(err)
			}
			request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://api.example/first", nil)
			if err != nil {
				t.Fatal(err)
			}
			if change == "url" {
				request.URL.Host = "other.example"
			} else {
				request.Host = "other.example"
			}
			closeError := errors.New("body close failed")
			body := &trackedBody{Reader: strings.NewReader("private request"), closeError: closeError}
			request.Body = body
			response, err := client.Do(request)
			if response != nil {
				_ = response.Body.Close()
			}
			if calls != 0 || !errors.Is(err, httpclient.ErrOriginNotAllowed) || !errors.Is(err, closeError) || body.closed != 1 {
				t.Fatalf("calls=%d body closes=%d error=%v", calls, body.closed, err)
			}
		})
	}
}

func TestClientOwnershipAndTransportCleanup(t *testing.T) {
	borrowed := &http.Client{Timeout: time.Second}
	owned, err := httpclient.New(t.Context(), httpclient.Config{BaseURL: "https://api.example", HTTPClient: borrowed})
	if err != nil {
		t.Fatal(err)
	}
	if owned == borrowed || borrowed.Transport != nil || borrowed.CheckRedirect != nil || borrowed.Timeout != time.Second {
		t.Fatal("constructor changed the borrowed client")
	}
	transport := &recordingTransport{}
	owned, err = httpclient.New(t.Context(), httpclient.Config{BaseURL: "https://api.example", HTTPClient: &http.Client{Transport: transport}})
	if err != nil {
		t.Fatal(err)
	}
	owned.CloseIdleConnections()
	if transport.closed != 1 {
		t.Fatal("transport wrapper hid connection cleanup")
	}
}

func TestInvalidOriginsAreRejectedAtConstruction(t *testing.T) {
	for _, endpoint := range []string{"", "/relative", "file:///tmp/credential", "https:///missing-host", "https://user:password@api.example"} {
		if client, err := httpclient.New(t.Context(), httpclient.Config{BaseURL: endpoint}); err == nil || client != nil {
			t.Fatalf("invalid endpoint %q: client=%v error=%v", endpoint, client, err)
		}
	}
	_, err := httpclient.New(t.Context(), httpclient.Config{BaseURL: "https://api.example/%zz"})
	var parseError *url.Error
	if !errors.As(err, &parseError) {
		t.Fatalf("endpoint parse cause was lost: %v", err)
	}
}

func TestTypedNilTransportUsesTheDefaultWithoutMutatingTheHost(t *testing.T) {
	var transport *recordingTransport
	borrowed := &http.Client{Transport: transport}
	owned, err := httpclient.New(t.Context(), httpclient.Config{BaseURL: "https://api.example", HTTPClient: borrowed})
	if err != nil || owned == nil || owned.Transport == nil || borrowed.Transport != transport {
		t.Fatalf("typed nil transport: client=%v error=%v", owned, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.example/", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := owned.Do(request)
	if response != nil {
		_ = response.Body.Close()
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("default transport did not retain cancellation: %v", err)
	}
}
