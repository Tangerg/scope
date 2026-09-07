package brave

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Tangerg/scope/tools/web"
)

func TestSearch(t *testing.T) {
	if _, err := NewClient(Config{}); err == nil {
		t.Fatal("NewClient accepted an empty API key")
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/web/search" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("X-Subscription-Token"); got != "test-key" {
			t.Errorf("X-Subscription-Token = %q", got)
		}
		if got := r.URL.Query().Get("q"); got != "scope (site:example.com OR site:example.org)" {
			t.Errorf("q = %q", got)
		}
		if got := r.URL.Query().Get("count"); got != "20" {
			t.Errorf("count = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"query":{"original":"scope"},"web":{"results":[{"title":"Scope","url":"https://example.com","description":"cat","page_age":"2026-08-03T00:00:00Z"}]}}`))
	}))
	t.Cleanup(server.Close)

	client, err := NewClient(Config{APIKey: "test-key", BaseURL: server.URL, HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Search(t.Context(), &web.SearchRequest{Query: "scope", MaxResults: 20, AllowedDomains: []string{"example.com", "example.org"}})
	if err != nil {
		t.Fatal(err)
	}
	if response.Query != "scope" || len(response.Results) != 1 || response.Results[0].Snippet != "cat" {
		t.Fatalf("response = %#v", response)
	}
}
