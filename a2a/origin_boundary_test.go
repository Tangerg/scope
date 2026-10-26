package a2a

import (
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

type originBoundaryTransport struct {
	calls  int
	closed bool
}

func (o *originBoundaryTransport) RoundTrip(*http.Request) (*http.Response, error) {
	o.calls++
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("{}")), Header: make(http.Header)}, nil
}

func (o *originBoundaryTransport) CloseIdleConnections() { o.closed = true }

type rejectedRequestBody struct {
	closed bool
	cause  error
}

func (r *rejectedRequestBody) Read([]byte) (int, error) { return 0, io.EOF }
func (r *rejectedRequestBody) Close() error             { r.closed = true; return r.cause }

func TestOriginPolicyChecksRequestAuthorityAndClosesRejectedBodies(t *testing.T) {
	for _, test := range []struct {
		name, endpoint, host string
	}{
		{name: "destination", endpoint: "https://other.example/rpc"},
		{name: "authority", endpoint: "https://agent.example/rpc", host: "other.example"},
		{name: "userinfo", endpoint: "https://name:password@agent.example/rpc"},
	} {
		t.Run(test.name, func(t *testing.T) {
			policy, err := newEndpointOriginPolicy("https://agent.example/card", nil)
			if err != nil {
				t.Fatal(err)
			}
			transport := &originBoundaryTransport{}
			client := policy.rpcOrigins.restrict(&http.Client{Transport: transport})
			closeErr := errors.New("request body close failed")
			body := &rejectedRequestBody{cause: closeErr}
			request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, test.endpoint, body)
			if err != nil {
				t.Fatal(err)
			}
			if test.host != "" {
				request.Host = test.host
			}
			_, err = client.Transport.RoundTrip(request)
			if !errors.Is(err, ErrOriginNotAllowed) || !errors.Is(err, closeErr) {
				t.Fatalf("RoundTrip error = %v; want origin rejection and close failure", err)
			}
			if transport.calls != 0 || !body.closed {
				t.Fatalf("calls=%d closed=%t", transport.calls, body.closed)
			}
		})
	}
}

func TestOriginPolicyRechecksRedirectCallbackAuthority(t *testing.T) {
	policy, err := newEndpointOriginPolicy("https://agent.example/card", nil)
	if err != nil {
		t.Fatal(err)
	}
	client := policy.rpcOrigins.restrict(&http.Client{CheckRedirect: func(request *http.Request, _ []*http.Request) error {
		request.Host = "other.example"
		return nil
	}})
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://agent.example/card", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.CheckRedirect(request, nil); !errors.Is(err, ErrOriginNotAllowed) {
		t.Fatalf("redirect error = %v; want origin rejection", err)
	}
}

func TestOriginPolicyRejectsEmbeddedCredentialsAndEmptyQuery(t *testing.T) {
	if _, err := newEndpointOriginPolicy("https://name:password@agent.example/card", nil); !errors.Is(err, ErrInvalidCardURL) {
		t.Fatalf("card origin error = %v", err)
	}
	for _, origin := range []string{"https://name:password@agent.example", "https://agent.example?"} {
		if _, err := newEndpointOriginPolicy("https://agent.example/card", []string{origin}); !errors.Is(err, ErrInvalidRPCOrigin) {
			t.Fatalf("RPC origin %q error = %v", origin, err)
		}
	}
}

func TestOriginPolicyPreservesTransportLifecycle(t *testing.T) {
	policy, err := newEndpointOriginPolicy("https://agent.example/card", nil)
	if err != nil {
		t.Fatal(err)
	}
	transport := &originBoundaryTransport{}
	client := policy.rpcOrigins.restrict(&http.Client{Transport: transport})
	client.CloseIdleConnections()
	if !transport.closed {
		t.Fatal("restricted client hid its transport's idle-connection cleanup")
	}
	request := &http.Request{URL: &url.URL{Scheme: "https", Host: "AGENT.EXAMPLE:443"}, Host: "agent.example"}
	response, err := client.Transport.RoundTrip(request)
	if err != nil {
		t.Fatal(err)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if transport.calls != 1 {
		t.Fatalf("allowed origin calls = %d", transport.calls)
	}
}
