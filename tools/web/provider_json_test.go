package web_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Tangerg/scope/tools/web"
	"github.com/Tangerg/scope/tools/web/brave"
	"github.com/Tangerg/scope/tools/web/exa"
	"github.com/Tangerg/scope/tools/web/firecrawl"
	"github.com/Tangerg/scope/tools/web/jina"
	"github.com/Tangerg/scope/tools/web/perplexity"
	"github.com/Tangerg/scope/tools/web/serper"
	"github.com/Tangerg/scope/tools/web/tavily"
)

func TestSearchProvidersRejectAmbiguousJSON(t *testing.T) {
	for _, test := range []struct {
		name      string
		newClient func(string, *http.Client) (web.Searcher, error)
	}{
		{"brave", func(url string, client *http.Client) (web.Searcher, error) {
			return brave.NewClient(brave.Config{APIKey: "test", BaseURL: url, HTTPClient: client})
		}},
		{"exa", func(url string, client *http.Client) (web.Searcher, error) {
			return exa.NewClient(exa.Config{APIKey: "test", BaseURL: url, HTTPClient: client})
		}},
		{"firecrawl", func(url string, client *http.Client) (web.Searcher, error) {
			return firecrawl.NewClient(firecrawl.Config{APIKey: "test", BaseURL: url, HTTPClient: client})
		}},
		{"jina", func(url string, client *http.Client) (web.Searcher, error) {
			return jina.NewClient(jina.Config{APIKey: "test", SearchBaseURL: url, FetchBaseURL: url, HTTPClient: client})
		}},
		{"perplexity", func(url string, client *http.Client) (web.Searcher, error) {
			return perplexity.NewClient(perplexity.Config{APIKey: "test", BaseURL: url, HTTPClient: client})
		}},
		{"serper", func(url string, client *http.Client) (web.Searcher, error) {
			return serper.NewClient(serper.Config{APIKey: "test", BaseURL: url, HTTPClient: client})
		}},
		{"tavily", func(url string, client *http.Client) (web.Searcher, error) {
			return tavily.NewClient(tavily.Config{APIKey: "test", BaseURL: url, HTTPClient: client})
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"success":false,"success":true}`))
			}))
			t.Cleanup(server.Close)
			client, err := test.newClient(server.URL, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			response, err := client.Search(t.Context(), &web.SearchRequest{Query: "test"})
			if response != nil || err == nil {
				t.Fatalf("ambiguous JSON accepted: response = %#v, error = %v", response, err)
			}
		})
	}
}
