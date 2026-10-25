package providerconformance_test

import (
	"context"
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

var webSearchers = map[string]func(context.Context) (web.Searcher, error){
	"brave": func(ctx context.Context) (web.Searcher, error) {
		return brave.NewClient(ctx, brave.Config{APIKey: "test"})
	},
	"exa": func(ctx context.Context) (web.Searcher, error) { return exa.NewClient(ctx, exa.Config{APIKey: "test"}) },
	"firecrawl": func(ctx context.Context) (web.Searcher, error) {
		return firecrawl.NewClient(ctx, firecrawl.Config{APIKey: "test"})
	},
	"jina": func(ctx context.Context) (web.Searcher, error) {
		return jina.NewClient(ctx, jina.Config{APIKey: "test"})
	},
	"perplexity": func(ctx context.Context) (web.Searcher, error) {
		return perplexity.NewClient(ctx, perplexity.Config{APIKey: "test"})
	},
	"serper": func(ctx context.Context) (web.Searcher, error) {
		return serper.NewClient(ctx, serper.Config{APIKey: "test"})
	},
	"tavily": func(ctx context.Context) (web.Searcher, error) {
		return tavily.NewClient(ctx, tavily.Config{APIKey: "test"})
	},
}

func TestWebSearchProvidersValidateRequests(t *testing.T) {
	for name, constructor := range webSearchers {
		t.Run(name, func(t *testing.T) {
			searcher, err := constructor(t.Context())
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
