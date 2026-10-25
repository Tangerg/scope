package web_test

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"testing"

	"github.com/Tangerg/scope/tools/web"
)

func TestSearchProvidersPreserveEmptyResultsAndRejectInvalidRecords(t *testing.T) {
	envelopes := map[string]string{
		"brave":      `{"web":{"results":[%s]}}`,
		"exa":        `{"results":[%s]}`,
		"firecrawl":  `{"success":true,"data":{"web":[%s]}}`,
		"jina":       `{"data":[%s]}`,
		"perplexity": `{"results":[%s]}`,
		"serper":     `{"organic":[%s]}`,
		"tavily":     `{"results":[%s]}`,
	}
	for _, provider := range testProviders {
		for _, record := range []string{"", "null", "{}"} {
			t.Run(provider.name+"/"+record, func(t *testing.T) {
				client := newTestProvider(t, provider, func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					_, _ = fmt.Fprintf(w, envelopes[provider.name], record)
				})
				response, err := client.Search(t.Context(), &web.SearchRequest{Query: "scope"})
				if record == "" {
					if err != nil || response == nil || len(response.Results) != 0 {
						t.Fatalf("explicit empty result: response=%v error=%v", response, err)
					}
					return
				}
				if response != nil || !errors.Is(err, web.ErrInvalidSearchResponse) {
					t.Fatalf("invalid record became success: response=%v error=%v", response, err)
				}
			})
		}
	}
}

func TestTavilyFetchedContentRequiresPresence(t *testing.T) {
	for _, sample := range []struct {
		body  string
		valid bool
	}{
		{`{"results":[{}]}`, false},
		{`{"results":[{"raw_content":null}]}`, false},
		{`{"results":[{"raw_content":""}]}`, true},
	} {
		t.Run(sample.body, func(t *testing.T) {
			index := slices.IndexFunc(testProviders, func(provider testProvider) bool { return provider.name == "tavily" })
			if index < 0 {
				t.Fatal("Tavily test provider is missing")
			}
			provider := testProviders[index]
			client := newTestProvider(t, provider, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(sample.body))
			})
			response, err := client.(web.Fetcher).Fetch(t.Context(), &web.FetchRequest{URL: "https://example.com"})
			if sample.valid {
				if err != nil || response == nil || response.Content != "" {
					t.Fatalf("explicit empty content: response=%v error=%v", response, err)
				}
			} else if response != nil || !errors.Is(err, web.ErrInvalidFetchResponse) {
				t.Fatalf("missing content became success: response=%v error=%v", response, err)
			}
		})
	}
}

func TestSearchProvidersDoNotInventEmptyResultsFromMissingPayloads(t *testing.T) {
	payloads := map[string][]string{
		"exa":        {`{}`, `{"results":null}`},
		"perplexity": {`{}`, `{"results":null}`},
		"tavily":     {`{}`, `{"results":null}`},
		"jina":       {`{}`, `{"data":null}`},
		"firecrawl":  {`{"success":true}`, `{"success":true,"data":null}`},
	}
	for _, provider := range testProviders {
		for _, payload := range payloads[provider.name] {
			t.Run(provider.name+"/"+payload, func(t *testing.T) {
				client := newTestProvider(t, provider, func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(payload))
				})
				response, err := client.Search(t.Context(), &web.SearchRequest{Query: "scope"})
				if response != nil || !errors.Is(err, web.ErrInvalidSearchResponse) {
					t.Fatalf("missing payload became success: response=%v error=%v", response, err)
				}
			})
		}
	}
}
