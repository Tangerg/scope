package modeltest

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"testing"
	"time"

	"github.com/samber/lo"
)

const (
	httpClientProbeURL     = "https://provider.example.test"
	httpClientProbeTimeout = time.Second
)

type HTTPClientContract struct {
	// Return each client initialized by the binding so independent protocol
	// clients cannot accidentally share a mutable configuration slot.
	Build func(t *testing.T, baseURL string, borrowed *http.Client) []*http.Client
}

// The default-transport probe changes process state and must run sequentially.
func RunHTTPClientContract(t *testing.T, contract HTTPClientContract) {
	t.Helper()
	if contract.Build == nil {
		t.Fatal("HTTP client contract requires a constructor")
	}
	for _, test := range []struct {
		name     string
		explicit bool
	}{
		{name: "default transport"},
		{name: "explicit transport", explicit: true},
	} {
		t.Run(test.name, func(t *testing.T) { contract.run(t, test.explicit) })
	}
}

func (h HTTPClientContract) run(t *testing.T, explicit bool) {
	t.Helper()
	transport := new(httpClientTransport)
	previousDefault := http.DefaultTransport
	http.DefaultTransport = transport
	t.Cleanup(func() { http.DefaultTransport = previousDefault })
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	redirectErr := errors.New("host redirect policy")
	borrowed := &http.Client{
		Timeout: httpClientProbeTimeout, Jar: jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { return redirectErr },
	}
	if explicit {
		borrowed.Transport = transport
	}
	observation := httpClientObservation{
		borrowed: borrowed, configuration: *borrowed,
		defaultTransport: transport, redirectErr: redirectErr,
	}
	owned := h.Build(t, httpClientProbeURL, borrowed)
	if err := observation.validate(owned); err != nil {
		t.Fatal(err)
	}
	for _, client := range append([]*http.Client{borrowed}, owned...) {
		response, err := client.Get(httpClientProbeURL)
		if err != nil {
			t.Fatal(err)
		}
		if err := response.Body.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if transport.calls != len(owned)+1 {
		t.Fatalf("transport calls = %d; want one per client and no construction I/O", transport.calls)
	}
}

type httpClientObservation struct {
	borrowed         *http.Client
	configuration    http.Client
	defaultTransport http.RoundTripper
	redirectErr      error
}

func (h httpClientObservation) validate(owned []*http.Client) error {
	if h.borrowed.Transport != h.configuration.Transport || h.borrowed.Jar != h.configuration.Jar || h.borrowed.Timeout != h.configuration.Timeout || http.DefaultTransport != h.defaultTransport {
		return errors.New("construction changed borrowed HTTP configuration")
	}
	request, err := http.NewRequest(http.MethodGet, httpClientProbeURL, nil)
	if err != nil {
		return err
	}
	if h.borrowed.CheckRedirect == nil || !errors.Is(h.borrowed.CheckRedirect(request, nil), h.redirectErr) {
		return errors.New("construction changed borrowed redirect policy")
	}
	if len(owned) == 0 {
		return errors.New("construction returned no owned HTTP clients")
	}
	seen := make(map[*http.Client]struct{}, len(owned))
	for index, client := range owned {
		if client == nil || client == h.borrowed {
			return fmt.Errorf("HTTP client %d has no independently owned configuration", index)
		}
		if _, exists := seen[client]; exists {
			return fmt.Errorf("HTTP client %d shares its configuration with another owned client", index)
		}
		seen[client] = struct{}{}
		if lo.IsNil(client.Transport) {
			return fmt.Errorf("HTTP client %d did not freeze its transport", index)
		}
		if client.Timeout != h.configuration.Timeout || client.Jar != h.configuration.Jar || client.CheckRedirect == nil || !errors.Is(client.CheckRedirect(request, nil), h.redirectErr) {
			return fmt.Errorf("HTTP client %d lost borrowed HTTP policy", index)
		}
	}
	return nil
}

type httpClientTransport struct{ calls int }

func (h *httpClientTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.Body != nil {
		defer request.Body.Close()
	}
	h.calls++
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: http.NoBody, Request: request}, nil
}
