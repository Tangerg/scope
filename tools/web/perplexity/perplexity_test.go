package perplexity

import (
	jsonv2 "encoding/json/v2"
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
		if r.Method != http.MethodPost || r.URL.Path != "/search" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization = %q", got)
		}
		var body struct {
			Query   string   `json:"query"`
			Results int      `json:"max_results"`
			Domains []string `json:"search_domain_filter"`
			Recency string   `json:"search_recency_filter"`
		}
		if err := jsonv2.UnmarshalRead(r.Body, &body); err != nil {
			t.Errorf("decode body: %v", err)
			return
		}
		if body.Query != "scope" || body.Results != 20 || body.Recency != "day" || len(body.Domains) != 1 || body.Domains[0] != "-blocked.example" {
			t.Errorf("body = %#v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"title":"Scope","url":"https://example.com","snippet":"cat","date":"2026-08-03"}]}`))
	}))
	t.Cleanup(server.Close)

	client, err := NewClient(Config{APIKey: "test-key", BaseURL: server.URL, HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Search(t.Context(), &web.SearchRequest{Query: "scope", MaxResults: 20, BlockedDomains: []string{"blocked.example"}, Recency: web.RecencyDay})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 1 || response.Results[0].PublishedTime.IsZero() {
		t.Fatalf("response = %#v", response)
	}
}
