package jina

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tangerg/scope/tools/web"
)

func TestSearchResultSnippetPreservesUTF8(t *testing.T) {
	content := strings.Repeat("界", maximumSnippetRunes+1)
	snippet := (&searchResult{Content: content}).snippet()
	if snippet != strings.Repeat("界", maximumSnippetRunes)+snippetEllipsis {
		t.Fatalf("snippet = %q", snippet)
	}
}

func TestSearch(t *testing.T) {
	if _, err := NewClient(Config{}); err == nil {
		t.Fatal("NewClient accepted an empty API key")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/scope agent" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.Header.Get("X-Respond-With"); got != "no-content" {
			t.Errorf("X-Respond-With = %q", got)
		}
		if got := r.URL.Query().Get("count"); got != "20" {
			t.Errorf("count = %q", got)
		}
		if got := r.URL.Query()["site"]; len(got) != 2 || got[0] != "example.com" || got[1] != "example.org" {
			t.Errorf("site = %#v", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"title":"Scope","url":"https://example.com","description":"cat","date":"2026-08-03"}]}`))
	}))
	t.Cleanup(server.Close)

	client, err := NewClient(Config{APIKey: "test-key", SearchBaseURL: server.URL, HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Search(t.Context(), &web.SearchRequest{
		Query:          "scope agent",
		MaxResults:     20,
		AllowedDomains: []string{"example.com", "example.org"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.Query != "scope agent" || len(response.Results) != 1 || response.Results[0].Snippet != "cat" {
		t.Fatalf("response = %#v", response)
	}
	for _, request := range []*web.SearchRequest{
		{Query: "scope", BlockedDomains: []string{"example.com"}},
		{Query: "scope", Recency: web.RecencyDay},
	} {
		if _, err := client.Search(t.Context(), request); !errors.Is(err, web.ErrUnsupportedFilter) {
			t.Fatalf("Search(%+v) error = %v, want ErrUnsupportedFilter", request, err)
		}
	}
}
