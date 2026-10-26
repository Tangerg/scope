package vectara

import (
	"errors"
	"io"
	"net/http"
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
	store, err := NewStore(t.Context(), StoreConfig{
		Endpoint: "https://api.example", APIKey: "credential", CorpusKey: "corpus",
		HTTPClient: &http.Client{Transport: transport}, DocumentBatcher: testBatcher{},
	})
	if store != nil || !errors.Is(err, httpclient.ErrOriginNotAllowed) || len(transport.destinations) != 1 {
		t.Fatalf("credential destinations=%v store=%v error=%v", transport.destinations, store, err)
	}
}

func TestEndpointRejectsEmptyQueryAndFragment(t *testing.T) {
	for _, endpoint := range []string{"https://api.example?", "https://api.example#", "https://api.example/base?", "https://api.example/base#"} {
		config := StoreConfig{Endpoint: endpoint, APIKey: "credential", CorpusKey: "corpus", DocumentBatcher: testBatcher{}}
		if err := config.Validate(); err == nil {
			t.Fatalf("endpoint %q can absorb the corpus resource path", endpoint)
		}
	}
}
