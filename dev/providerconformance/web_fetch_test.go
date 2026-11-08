package providerconformance_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/Tangerg/scope/core/tool"
	"github.com/Tangerg/scope/tools/web"
	"github.com/Tangerg/scope/tools/web/exa"
	"github.com/Tangerg/scope/tools/web/firecrawl"
	"github.com/Tangerg/scope/tools/web/jina"
	"github.com/Tangerg/scope/tools/web/tavily"
)

var webFetchers = map[string]func(context.Context, *http.Client) (web.Fetcher, error){
	"exa": func(ctx context.Context, client *http.Client) (web.Fetcher, error) {
		return exa.NewClient(ctx, exa.Config{APIKey: "test", HTTPClient: client})
	},
	"firecrawl": func(ctx context.Context, client *http.Client) (web.Fetcher, error) {
		return firecrawl.NewClient(ctx, firecrawl.Config{APIKey: "test", HTTPClient: client})
	},
	"jina": func(ctx context.Context, client *http.Client) (web.Fetcher, error) {
		return jina.NewClient(ctx, jina.Config{APIKey: "test", HTTPClient: client})
	},
	"tavily": func(ctx context.Context, client *http.Client) (web.Fetcher, error) {
		return tavily.NewClient(ctx, tavily.Config{APIKey: "test", HTTPClient: client})
	},
}

func TestWebFetchProvidersValidateRequests(t *testing.T) {
	for name, constructor := range webFetchers {
		t.Run(name, func(t *testing.T) {
			calls := 0
			transport := invocationBoundaryTransport(func(*http.Request) (*http.Response, error) {
				calls++
				return nil, errors.New("web conformance: rejected input reached HTTP")
			})
			provider, err := constructor(t.Context(), &http.Client{Transport: transport})
			if err != nil {
				t.Fatalf("construct provider: %v", err)
			}
			for _, test := range []struct {
				name    string
				request *web.FetchRequest
				want    error
			}{
				{name: "nil", want: web.ErrMissingFetchRequest},
				{name: "empty", request: &web.FetchRequest{}, want: web.ErrEmptyURL},
			} {
				t.Run(test.name, func(t *testing.T) {
					response, callErr := provider.Fetch(t.Context(), test.request)
					failure, definite := tool.ClassifyFailure(callErr)
					if response != nil || !definite || failure.Kind() != tool.FailureKindFailed || !errors.Is(failure.Cause(), test.want) || calls != 0 {
						t.Fatalf("Fetch(%s) response=%v error=%v calls=%d, want local failed admission with cause %v and no HTTP", test.name, response, callErr, calls, test.want)
					}
					if errors.Is(callErr, test.want) {
						t.Fatalf("Fetch(%s) exposed its diagnostic through the outcome boundary: %v", test.name, callErr)
					}
					wantFeedback := name + ": prepare fetch request: " + test.want.Error()
					if feedback, _ := failure.Output().Text(); feedback != wantFeedback {
						t.Fatalf("Fetch(%s) feedback=%q, want %q", test.name, feedback, wantFeedback)
					}
				})
			}
		})
	}
}
