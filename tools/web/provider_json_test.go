package web_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
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

type testProvider struct {
	name           string
	searchEnvelope string
	newClient      func(ctx context.Context, baseURL string, client *http.Client) (web.Searcher, error)
}

func (t testProvider) searchPayload(count int) string {
	var records []string
	for index := range count {
		records = append(records, fmt.Sprintf(`{"url":"https://example.com/%d","link":"https://example.com/%d"}`, index, index))
	}
	return fmt.Sprintf(t.searchEnvelope, strings.Join(records, ","))
}

var testProviders = []testProvider{
	{"brave", `{"web":{"results":[%s]}}`, func(ctx context.Context, url string, client *http.Client) (web.Searcher, error) {
		return brave.NewClient(ctx, brave.Config{APIKey: "test", BaseURL: url, HTTPClient: client})
	}},
	{"exa", `{"results":[%s]}`, func(ctx context.Context, url string, client *http.Client) (web.Searcher, error) {
		return exa.NewClient(ctx, exa.Config{APIKey: "test", BaseURL: url, HTTPClient: client})
	}},
	{"firecrawl", `{"success":true,"data":{"web":[%s]}}`, func(ctx context.Context, url string, client *http.Client) (web.Searcher, error) {
		return firecrawl.NewClient(ctx, firecrawl.Config{APIKey: "test", BaseURL: url, HTTPClient: client})
	}},
	{"jina", `{"data":[%s]}`, func(ctx context.Context, url string, client *http.Client) (web.Searcher, error) {
		return jina.NewClient(ctx, jina.Config{APIKey: "test", SearchBaseURL: url, FetchBaseURL: url, HTTPClient: client})
	}},
	{"perplexity", `{"results":[%s]}`, func(ctx context.Context, url string, client *http.Client) (web.Searcher, error) {
		return perplexity.NewClient(ctx, perplexity.Config{APIKey: "test", BaseURL: url, HTTPClient: client})
	}},
	{"serper", `{"organic":[%s]}`, func(ctx context.Context, url string, client *http.Client) (web.Searcher, error) {
		return serper.NewClient(ctx, serper.Config{APIKey: "test", BaseURL: url, HTTPClient: client})
	}},
	{"tavily", `{"results":[%s]}`, func(ctx context.Context, url string, client *http.Client) (web.Searcher, error) {
		return tavily.NewClient(ctx, tavily.Config{APIKey: "test", BaseURL: url, HTTPClient: client})
	}},
}

func newTestProvider(t *testing.T, provider testProvider, handler http.HandlerFunc) web.Searcher {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := provider.newClient(t.Context(), server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestSearchProvidersRejectAmbiguousJSON(t *testing.T) {
	for _, provider := range testProviders {
		t.Run(provider.name, func(t *testing.T) {
			client := newTestProvider(t, provider, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"success":false,"success":true}`))
			})
			response, err := client.Search(t.Context(), &web.SearchRequest{Query: "test"})
			if response != nil || err == nil {
				t.Fatalf("ambiguous JSON accepted: response = %#v, error = %v", response, err)
			}
		})
	}
}

func TestProvidersRejectSuccessWithoutJSONBody(t *testing.T) {
	responses := map[string]http.HandlerFunc{
		"html": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(`<html>maintenance</html>`))
		},
		"no content": func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		},
	}
	for _, provider := range testProviders {
		for name, handler := range responses {
			t.Run(provider.name+"/"+name, func(t *testing.T) {
				client := newTestProvider(t, provider, handler)
				searchResponse, err := client.Search(t.Context(), &web.SearchRequest{Query: "test"})
				if searchResponse != nil || err == nil {
					t.Fatalf("Search accepted a body without JSON: response = %#v, error = %v", searchResponse, err)
				}
				fetcher, ok := client.(web.Fetcher)
				if !ok {
					return
				}
				fetchResponse, err := fetcher.Fetch(t.Context(), &web.FetchRequest{URL: "https://example.com", Format: web.FormatMarkdown})
				if fetchResponse != nil || err == nil {
					t.Fatalf("Fetch accepted a body without JSON: response = %#v, error = %v", fetchResponse, err)
				}
			})
		}
	}
}
