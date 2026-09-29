package providerconformance_test

import (
	"errors"
	"testing"

	"github.com/Tangerg/scope/tools/web"
	"github.com/Tangerg/scope/tools/web/exa"
	"github.com/Tangerg/scope/tools/web/firecrawl"
	"github.com/Tangerg/scope/tools/web/jina"
	"github.com/Tangerg/scope/tools/web/tavily"
)

var webFetchers = map[string]func() (web.Fetcher, error){
	"exa":       func() (web.Fetcher, error) { return exa.NewClient(exa.Config{APIKey: "test"}) },
	"firecrawl": func() (web.Fetcher, error) { return firecrawl.NewClient(firecrawl.Config{APIKey: "test"}) },
	"jina":      func() (web.Fetcher, error) { return jina.NewClient(jina.Config{APIKey: "test"}) },
	"tavily":    func() (web.Fetcher, error) { return tavily.NewClient(tavily.Config{APIKey: "test"}) },
}

func TestWebFetchProvidersValidateRequests(t *testing.T) {
	for name, constructor := range webFetchers {
		t.Run(name, func(t *testing.T) {
			fetcher, err := constructor()
			if err != nil {
				t.Fatalf("construct provider: %v", err)
			}
			if _, err := fetcher.Fetch(t.Context(), nil); !errors.Is(err, web.ErrMissingFetchRequest) {
				t.Fatalf("Fetch(nil) error = %v, want ErrMissingFetchRequest", err)
			}
			if _, err := fetcher.Fetch(t.Context(), &web.FetchRequest{}); !errors.Is(err, web.ErrEmptyURL) {
				t.Fatalf("Fetch(empty) error = %v, want ErrEmptyURL", err)
			}
		})
	}
}
