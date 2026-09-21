package exa

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
		if got := r.Header.Get("x-api-key"); got != "test-key" {
			t.Errorf("x-api-key = %q", got)
		}
		var body struct {
			Query      string   `json:"query"`
			Type       string   `json:"type"`
			NumResults int      `json:"numResults"`
			Domains    []string `json:"includeDomains"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode body: %v", err)
			return
		}
		if body.Query != "scope" || body.Type != "fast" || body.NumResults != 20 || len(body.Domains) != 1 {
			t.Errorf("body = %#v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"title":"Scope","url":"https://example.com","author":"example","favicon":"https://example.com/favicon.ico","highlights":["highlight"]}]}`))
	}))
	t.Cleanup(server.Close)

	client, err := NewClient(Config{APIKey: "test-key", BaseURL: server.URL, HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Search(t.Context(), &web.SearchRequest{Query: "scope", MaxResults: 20, AllowedDomains: []string{"example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	if response.Query != "scope" || len(response.Results) != 1 || response.Results[0].Snippet != "highlight" {
		t.Fatalf("response = %#v", response)
	}
}

func TestFetchRequestsTextAndRequiresItsPresence(t *testing.T) {
	for _, format := range []web.ContentFormat{"", web.FormatText, web.FormatMarkdown, web.FormatHTML} {
		for _, body := range []string{`{"results":[{}]}`, `{"results":[{"text":""}]}`, `{"results":[{"text":"content"}]}`} {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request map[string]json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
				}
				if _, exists := request["text"]; !exists {
					t.Error("text extraction was not requested")
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(body))
			}))
			client, err := NewClient(Config{APIKey: "test", BaseURL: server.URL})
			if err != nil {
				t.Fatal(err)
			}
			response, err := client.Fetch(t.Context(), &web.FetchRequest{URL: "https://example.com", Format: format})
			server.Close()
			if (err != nil) != (body == `{"results":[{}]}`) {
				t.Fatalf("body=%s response=%v err=%v", body, response, err)
			}
		}
	}
}
