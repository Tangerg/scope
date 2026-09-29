package providerconformance_test

import (
	"errors"
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

var webSearchers = map[string]func() (web.Searcher, error){
	"brave":      func() (web.Searcher, error) { return brave.NewClient(brave.Config{APIKey: "test"}) },
	"exa":        func() (web.Searcher, error) { return exa.NewClient(exa.Config{APIKey: "test"}) },
	"firecrawl":  func() (web.Searcher, error) { return firecrawl.NewClient(firecrawl.Config{APIKey: "test"}) },
	"jina":       func() (web.Searcher, error) { return jina.NewClient(jina.Config{APIKey: "test"}) },
	"perplexity": func() (web.Searcher, error) { return perplexity.NewClient(perplexity.Config{APIKey: "test"}) },
	"serper":     func() (web.Searcher, error) { return serper.NewClient(serper.Config{APIKey: "test"}) },
	"tavily":     func() (web.Searcher, error) { return tavily.NewClient(tavily.Config{APIKey: "test"}) },
}

func TestWebSearchProvidersValidateRequests(t *testing.T) {
	for name, constructor := range webSearchers {
		t.Run(name, func(t *testing.T) {
			searcher, err := constructor()
			if err != nil {
				t.Fatalf("construct provider: %v", err)
			}
			if _, err := searcher.Search(t.Context(), nil); !errors.Is(err, web.ErrMissingSearchRequest) {
				t.Fatalf("Search(nil) error = %v, want ErrMissingSearchRequest", err)
			}
			if _, err := searcher.Search(t.Context(), &web.SearchRequest{}); !errors.Is(err, web.ErrEmptyQuery) {
				t.Fatalf("Search(empty) error = %v, want ErrEmptyQuery", err)
			}
		})
	}
}
