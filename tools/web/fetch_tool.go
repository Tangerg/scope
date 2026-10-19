package web

import (
	"context"
	"errors"
	"fmt"

	"github.com/samber/lo"

	toolcontract "github.com/Tangerg/scope/core/tool"
)

var _ toolcontract.Tool = (*FetchTool)(nil)

type FetchTool struct {
	readOnlyTool
	fetcher Fetcher
}

func NewFetchTool(fetcher Fetcher) (*FetchTool, error) {
	if lo.IsNil(fetcher) {
		return nil, ErrMissingFetcher
	}
	f := &FetchTool{fetcher: fetcher}
	inner, err := toolcontract.NewFunc(
		toolcontract.FuncConfig{Name: "web_fetch", Description: webFetchDescription},
		f.fetch,
	)
	if err != nil {
		return nil, fmt.Errorf("web: build fetch tool: %w", err)
	}
	f.inner = inner
	return f, nil
}

func (f *FetchTool) fetch(ctx context.Context, request FetchRequest) (*FetchResponse, error) {
	prepared, err := request.Prepare()
	if err != nil {
		return nil, inputFailure(err)
	}
	response, err := f.fetcher.Fetch(ctx, prepared)
	if err != nil {
		// A provider rejects an unsupported format before any backend call, so
		// the outcome is a definite input failure, not an uncertain attempt.
		if errors.Is(err, ErrUnsupportedFormat) {
			return nil, inputFailure(err)
		}
		return nil, fmt.Errorf("web: execute fetch: %w", err)
	}
	if validationErr := response.Validate(); validationErr != nil {
		return nil, fmt.Errorf("web: validate fetch response: %w", validationErr)
	}
	return response, nil
}

const webFetchDescription = `Fetch a fully formed HTTP(S) URL. Returns page content in markdown (default), html, or text format.
Use this to read a supplied URL or inspect a search result beyond its snippet.
Network access, authentication, redirects, and JavaScript rendering depend on the configured fetcher.`
