package a2a

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	sdka2a "github.com/a2aproject/a2a-go/v2/a2a"
)

func TestDialBoundsAgentCardResolution(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		transport := &cardTimeoutTransport{}
		const timeout = 20 * time.Millisecond
		start := time.Now()
		_, _, err := dial(t.Context(), Endpoint{
			CardURL: "https://agent.example", CardTimeout: timeout,
			HTTPClient: &http.Client{Transport: transport},
		})
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("dial error = %v, want context deadline exceeded", err)
		}
		if !transport.started || time.Since(start) != timeout || t.Context().Err() != nil {
			t.Fatalf("resolution started=%t elapsed=%s parent error=%v", transport.started, time.Since(start), t.Context().Err())
		}
	})
}

type cardTimeoutTransport struct {
	started bool
}

func (c *cardTimeoutTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	c.started = true
	<-request.Context().Done()
	return nil, request.Context().Err()
}

func TestDialRejectsInvalidCardConfiguration(t *testing.T) {
	tests := []struct {
		name     string
		endpoint Endpoint
		want     error
	}{
		{name: "relative card URL", endpoint: Endpoint{CardURL: "/agent"}, want: ErrInvalidCardURL},
		{name: "non-HTTP card URL", endpoint: Endpoint{CardURL: "file:///tmp/card.json"}, want: ErrInvalidCardURL},
		{name: "negative timeout", endpoint: Endpoint{CardURL: "https://agent.example", CardTimeout: -time.Second}, want: ErrInvalidCardTimeout},
		{name: "RPC origin with path", endpoint: Endpoint{CardURL: "https://agent.example", AllowedRPCOrigins: []string{"https://rpc.example/path"}}, want: ErrInvalidRPCOrigin},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := dial(t.Context(), test.endpoint)
			if !errors.Is(err, test.want) {
				t.Fatalf("dial error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestOriginPolicyPreservesConfigurationErrorCauses(t *testing.T) {
	tests := []struct {
		name string
		run  func() error
		want error
	}{
		{
			name: "card URL",
			run: func() error {
				_, err := newEndpointOriginPolicy("https://%zz", nil)
				return err
			},
			want: ErrInvalidCardURL,
		},
		{
			name: "RPC origin",
			run: func() error {
				_, err := newEndpointOriginPolicy("https://agent.example", []string{"https://%zz"})
				return err
			},
			want: ErrInvalidRPCOrigin,
		},
		{
			name: "supported interface",
			run: func() error {
				policy, err := newEndpointOriginPolicy("https://agent.example", nil)
				if err != nil {
					return err
				}
				return policy.validateCard(&sdka2a.AgentCard{SupportedInterfaces: []*sdka2a.AgentInterface{
					sdka2a.NewAgentInterface("https://%zz", sdka2a.TransportProtocolJSONRPC),
				}})
			},
			want: ErrInvalidCard,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.run()
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			var escape url.EscapeError
			if !errors.As(err, &escape) {
				t.Fatalf("error cause = %T, want url.EscapeError", err)
			}
		})
	}
}

func TestDialRejectsCrossOriginAgentCardRedirect(t *testing.T) {
	var targetHit atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		targetHit.Store(true)
	}))
	t.Cleanup(target.Close)
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+r.URL.Path, http.StatusFound)
	}))
	t.Cleanup(source.Close)

	_, _, err := dial(t.Context(), Endpoint{CardURL: source.URL})
	if !errors.Is(err, ErrOriginNotAllowed) {
		t.Fatalf("dial error = %v, want ErrOriginNotAllowed", err)
	}
	if targetHit.Load() {
		t.Fatal("cross-origin Agent Card redirect reached target")
	}
}

func TestDialRequiresExplicitTrustForCrossOriginRPC(t *testing.T) {
	rpc := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(rpc.Close)
	card := testAgentCard(rpc.URL)
	cardServer := serveAgentCard(t, card)

	_, _, err := dial(t.Context(), Endpoint{CardURL: cardServer.URL})
	if !errors.Is(err, ErrOriginNotAllowed) {
		t.Fatalf("strict dial error = %v, want ErrOriginNotAllowed", err)
	}

	client, _, err := dial(t.Context(), Endpoint{CardURL: cardServer.URL, AllowedRPCOrigins: []string{rpc.URL}})
	if err != nil {
		t.Fatalf("explicitly trusted dial: %v", err)
	}
	if err := client.Destroy(); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
}

func TestRestrictedHTTPClientDoesNotMutateCallerClient(t *testing.T) {
	var redirectCalls atomic.Int32
	base := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		redirectCalls.Add(1)
		return nil
	}}
	policy, err := newEndpointOriginPolicy("https://agent.example", nil)
	if err != nil {
		t.Fatalf("newEndpointOriginPolicy: %v", err)
	}
	restricted := policy.cardOrigins.restrict(base)
	if restricted == base {
		t.Fatal("originSet.restrict returned caller-owned client")
	}
	if base.Transport != nil || base.CheckRedirect == nil {
		t.Fatal("originSet.restrict mutated caller-owned client")
	}
	req, err := http.NewRequest(http.MethodGet, "https://other.example/card", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if _, err := restricted.Do(req); !errors.Is(err, ErrOriginNotAllowed) {
		t.Fatalf("Do error = %v, want ErrOriginNotAllowed", err)
	}
	if redirectCalls.Load() != 0 {
		t.Fatalf("caller redirect policy invoked %d time(s) for blocked initial request", redirectCalls.Load())
	}
}

func testAgentCard(rpcURL string) *sdka2a.AgentCard {
	return &sdka2a.AgentCard{
		Name: "test",
		SupportedInterfaces: []*sdka2a.AgentInterface{
			sdka2a.NewAgentInterface(rpcURL, sdka2a.TransportProtocolJSONRPC),
		},
	}
}

func serveAgentCard(t *testing.T, card *sdka2a.AgentCard) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(card); err != nil {
			t.Errorf("encode Agent Card: %v", err)
		}
	}))
	t.Cleanup(server.Close)
	return server
}
