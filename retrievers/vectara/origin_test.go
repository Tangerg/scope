package vectara

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/httpclient"
)

type redirectTransport struct{ destinations []string }

func (r *redirectTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	r.destinations = append(r.destinations, request.URL.Host+":"+request.Header.Get("x-api-key"))
	status := http.StatusTemporaryRedirect
	if len(r.destinations) > 1 {
		status = http.StatusOK
	}
	return &http.Response{
		StatusCode: status, Header: http.Header{"Location": {"https://other.example/corpus"}},
		Body: io.NopCloser(strings.NewReader(`{}`)), Request: request,
	}, nil
}

func TestCorpusBindingCannotRedirectTheAPIKey(t *testing.T) {
	transport := &redirectTransport{}
	store, err := New(t.Context(), Config{
		Endpoint: "https://api.example", APIKey: "credential", CorpusKey: "corpus",
		HTTPClient: &http.Client{Transport: transport},
	})
	if store != nil || !errors.Is(err, httpclient.ErrOriginNotAllowed) || len(transport.destinations) != 1 {
		t.Fatalf("credential destinations=%v store=%v error=%v", transport.destinations, store, err)
	}
}

func TestEndpointRejectsEmptyQueryAndFragment(t *testing.T) {
	for _, endpoint := range []string{"https://api.example?", "https://api.example#", "https://api.example/base?", "https://api.example/base#"} {
		config := Config{Endpoint: endpoint, APIKey: "credential", CorpusKey: "corpus"}
		if err := config.Validate(); err == nil {
			t.Fatalf("endpoint %q can absorb the corpus resource path", endpoint)
		}
	}
}

// A redirect within the service origin could still change corpus membership.
func TestCorpusBindingRejectsSameOriginRedirect(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		http.Redirect(w, r, "/v2/corpora/other", http.StatusTemporaryRedirect)
	}))
	t.Cleanup(server.Close)
	c, err := New(t.Context(), Config{Endpoint: server.URL, APIKey: "secret", CorpusKey: "corpus", HTTPClient: server.Client()})
	if c != nil || err == nil || len(paths) != 1 {
		t.Fatalf("scope redirect: %v %v", paths, err)
	}
}

func TestEndpointRequiresAHostButAllowsTheConfiguredBasePath(t *testing.T) {
	for _, sample := range []struct {
		endpoint string
		valid    bool
	}{
		{endpoint: "", valid: true},
		{endpoint: "http://service.example", valid: true},
		{endpoint: "https://service.example/base", valid: true},
		{endpoint: "http://:80"},
		{endpoint: "http://[]:80"},
		{endpoint: "https://service.example/base?scope=a"},
		{endpoint: "https://service.example/base#fragment"},
		{endpoint: "https://user@service.example/base"},
	} {
		t.Run(sample.endpoint, func(t *testing.T) {
			config := Config{Endpoint: sample.endpoint, APIKey: "credential", CorpusKey: "corpus"}
			if err := config.Validate(); (err == nil) != sample.valid {
				t.Fatalf("endpoint %q: valid=%t, error=%v", sample.endpoint, sample.valid, err)
			}
		})
	}
}
