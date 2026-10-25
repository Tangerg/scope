package providerconformance_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Tangerg/scope/tools/web"
	"github.com/Tangerg/scope/tools/web/exa"
	"github.com/Tangerg/scope/tools/web/firecrawl"
	"github.com/Tangerg/scope/tools/web/jina"
	"github.com/Tangerg/scope/tools/web/tavily"
)

var webFetchers = map[string]func(context.Context) (web.Fetcher, error){
	"exa": func(ctx context.Context) (web.Fetcher, error) { return exa.NewClient(ctx, exa.Config{APIKey: "test"}) },
	"firecrawl": func(ctx context.Context) (web.Fetcher, error) {
		return firecrawl.NewClient(ctx, firecrawl.Config{APIKey: "test"})
	},
	"jina": func(ctx context.Context) (web.Fetcher, error) {
		return jina.NewClient(ctx, jina.Config{APIKey: "test"})
	},
	"tavily": func(ctx context.Context) (web.Fetcher, error) {
		return tavily.NewClient(ctx, tavily.Config{APIKey: "test"})
	},
}

func TestWebFetchProvidersValidateRequests(t *testing.T) {
	for name, constructor := range webFetchers {
		t.Run(name, func(t *testing.T) {
			fetcher, err := constructor(t.Context())
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
