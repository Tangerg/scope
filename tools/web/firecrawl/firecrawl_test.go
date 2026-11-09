package firecrawl

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Tangerg/scope/core/tool"
	"github.com/Tangerg/scope/tools/web"
)

func TestSearchAdmitsQueryLengthBeforeHTTP(t *testing.T) {
	for _, test := range []struct {
		name      string
		query     string
		wantError bool
	}{
		{name: "ASCII limit", query: strings.Repeat("x", 500)},
		{name: "ASCII overflow", query: strings.Repeat("x", 501), wantError: true},
		{name: "Unicode limit", query: strings.Repeat("界", 500)},
		{name: "Unicode overflow", query: strings.Repeat("界", 501), wantError: true},
		{name: "supplementary characters", query: strings.Repeat("🌍", 500)},
		{name: "trimmed query", query: "  " + strings.Repeat("x", 500) + "  "},
	} {
		t.Run(test.name, func(t *testing.T) {
			var requests atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"success":true,"data":{"web":[]}}`))
			}))
			t.Cleanup(server.Close)
			client, err := NewClient(t.Context(), Config{APIKey: "test", BaseURL: server.URL, HTTPClient: server.Client()})
			if err != nil {
				t.Fatal(err)
			}
			response, err := client.Search(t.Context(), &web.SearchRequest{Query: test.query})
			if test.wantError {
				failure, correctable := tool.ClassifyFailure(err)
				if response != nil || !correctable || failure.Kind() != tool.FailureKindFailed || !errors.Is(failure.Cause(), web.ErrInvalidQuery) {
					t.Fatalf("Search = %v, %v; want correctable invalid query", response, err)
				}
				if requests.Load() != 0 {
					t.Fatal("invalid query reached the provider")
				}
				return
			}
			if err != nil || response == nil || requests.Load() != 1 {
				t.Fatalf("Search = %v, %v; requests = %d", response, err, requests.Load())
			}
		})
	}
}

func TestSearch(t *testing.T) {
	if _, err := NewClient(t.Context(), Config{}); err == nil {
		t.Fatal("NewClient accepted an empty API key")
	}
	for _, test := range []struct {
		name    string
		allowed []string
		blocked []string
	}{
		{name: "allowed union", allowed: []string{"example.com", "example.org"}},
		{name: "blocked domain", blocked: []string{"blocked.example"}},
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
					Query          string   `json:"query"`
					Limit          int      `json:"limit"`
					Tbs            string   `json:"tbs"`
					IncludeDomains []string `json:"includeDomains"`
					ExcludeDomains []string `json:"excludeDomains"`
				}
				if err := jsonv2.UnmarshalRead(r.Body, &body); err != nil {
					t.Errorf("decode body: %v", err)
					return
				}
				if body.Query != "scope" || body.Limit != 20 || body.Tbs != "qdr:w" ||
					!slices.Equal(body.IncludeDomains, test.allowed) || !slices.Equal(body.ExcludeDomains, test.blocked) {
					t.Errorf("body = %#v", body)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"success":true,"data":{"web":[{"title":"Scope","url":"https://example.com","description":"cat"}]}}`))
			}))
			t.Cleanup(server.Close)

			client, err := NewClient(t.Context(), Config{APIKey: "test-key", BaseURL: server.URL, HTTPClient: server.Client()})
			if err != nil {
				t.Fatal(err)
			}
			response, err := client.Search(t.Context(), &web.SearchRequest{Query: "scope", MaxResults: 20, AllowedDomains: test.allowed, BlockedDomains: test.blocked, Recency: web.RecencyWeek})
			if err != nil {
				t.Fatal(err)
			}
			if response.Query != "scope" || len(response.Results) != 1 || response.Results[0].Title != "Scope" {
				t.Fatalf("response = %#v", response)
			}
		})
	}
}

func TestFetchDoesNotSubstituteMarkdownForHTML(t *testing.T) {
	for _, sample := range []struct {
		body      string
		wantError bool
	}{
		{`{"success":true,"data":{"markdown":"wrong format"}}`, true},
		{`{"success":true,"data":{"html":""}}`, false},
		{`{"success":true,"data":{"html":"<p>right</p>"}}`, false},
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(sample.body))
		}))
		client, err := NewClient(t.Context(), Config{APIKey: "test", BaseURL: server.URL})
		if err != nil {
			t.Fatal(err)
		}
		response, err := client.Fetch(t.Context(), &web.FetchRequest{URL: "https://example.com", Format: web.FormatHTML})
		server.Close()
		if (err != nil) != sample.wantError {
			t.Fatalf("response=%v err=%v", response, err)
		}
	}
}
