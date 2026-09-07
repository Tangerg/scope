package firecrawl

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Tangerg/scope/tools/web"
)

func TestSearch(t *testing.T) {
	if _, err := NewClient(Config{}); err == nil {
		t.Fatal("NewClient accepted an empty API key")
	}
	for _, test := range []struct {
		name    string
		allowed []string
		blocked []string
		query   string
	}{
		{name: "allowed union", allowed: []string{"example.com", "example.org"}, query: "scope (site:example.com OR site:example.org)"},
		{name: "blocked domain", blocked: []string{"blocked.example"}, query: "scope -site:blocked.example"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/search" {
					t.Errorf("request = %s %s", r.Method, r.URL.Path)
				}
				if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
					t.Errorf("Authorization = %q", got)
				}
				var body struct {
					Query string `json:"query"`
					Limit int    `json:"limit"`
					Tbs   string `json:"tbs"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Errorf("decode body: %v", err)
					return
				}
				if body.Query != test.query || body.Limit != 20 || body.Tbs != "qdr:w" {
					t.Errorf("body = %#v", body)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"success":true,"data":{"web":[{"title":"Scope","url":"https://example.com","description":"cat"}]}}`))
			}))
			t.Cleanup(server.Close)

			client, err := NewClient(Config{APIKey: "test-key", BaseURL: server.URL, HTTPClient: server.Client()})
			if err != nil {
				t.Fatal(err)
			}
			response, err := client.Search(t.Context(), &web.SearchRequest{Query: "scope", MaxResults: 20, AllowedDomains: test.allowed, BlockedDomains: test.blocked, Recency: web.RecencyWeek})
			if err != nil {
				t.Fatal(err)
			}
			if len(response.Results) != 1 || response.Results[0].Title != "Scope" {
				t.Fatalf("response = %#v", response)
			}
		})
	}
}
