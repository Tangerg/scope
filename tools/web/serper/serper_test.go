package serper

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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/search" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("X-API-KEY"); got != "test-key" {
			t.Errorf("X-API-KEY = %q", got)
		}
		var body struct {
			Query       string `json:"q"`
			Num         int    `json:"num"`
			Autocorrect bool   `json:"autocorrect"`
			Tbs         string `json:"tbs"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode body: %v", err)
			return
		}
		if body.Query != "scope (site:example.com OR site:example.org)" || body.Num != 3 || !body.Autocorrect || body.Tbs != "qdr:m" {
			t.Errorf("body = %#v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"searchParameters":{"q":"scope"},"organic":[{"title":"Scope","link":"https://example.com","snippet":"cat","date":"2026-08-03"}]}`))
	}))
	t.Cleanup(server.Close)

	client, err := NewClient(Config{APIKey: "test-key", BaseURL: server.URL, HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Search(t.Context(), &web.SearchRequest{Query: "scope", MaxResults: 3, AllowedDomains: []string{"example.com", "example.org"}, Recency: web.RecencyMonth})
	if err != nil {
		t.Fatal(err)
	}
	if response.Query != "scope" || len(response.Results) != 1 {
		t.Fatalf("response = %#v", response)
	}
}
