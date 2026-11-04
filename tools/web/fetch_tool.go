package web

import (
	"context"
	"errors"
	"fmt"

	"github.com/samber/lo"

	toolcontract "github.com/Tangerg/scope/core/tool"
	"github.com/Tangerg/scope/tools/internal/toolresult"
)

const webFetchToolName = "web_fetch"

var _ toolcontract.Tool = (*FetchTool)(nil)

// fetchFunc is embedded under an unexported name so the tool keeps its
// Func private while promoting the Tool methods.
type fetchFunc = toolcontract.Func[FetchRequest, *FetchResponse]

type FetchTool struct {
	fetchFunc
	fetcher Fetcher
}

func NewFetchTool(fetcher Fetcher) (*FetchTool, error) {
	if lo.IsNil(fetcher) {
		return nil, ErrMissingFetcher
	}
	f := &FetchTool{fetcher: fetcher}
	typed, err := toolcontract.NewFunc(
		toolcontract.FuncConfig{Effect: toolcontract.EffectSafe, Name: webFetchToolName, Description: webFetchDescription},
		f.fetch,
	)
	if err != nil {
		return nil, fmt.Errorf("web: build fetch tool: %w", err)
	}
	f.fetchFunc = typed
	return f, nil
}

func (f *FetchTool) fetch(ctx context.Context, request FetchRequest) (*FetchResponse, error) {
	prepared, err := request.Prepare()
	if err != nil {
		return nil, toolresult.Correctable(err.Error(), err)
	}
	response, err := f.fetcher.Fetch(ctx, prepared)
	if err != nil {
		// An unsupported format is rejected before any backend call: definite, not uncertain.
		if errors.Is(err, ErrUnsupportedFormat) {
			return nil, toolresult.Correctable(err.Error(), err)
		}
		return nil, fmt.Errorf("web.fetch: %w", err)
	}
	if validationErr := response.ValidateFor(prepared); validationErr != nil {
		return nil, fmt.Errorf("web: validate fetch response: %w", validationErr)
	}
	return response, nil
}

const webFetchDescription = `Fetch a fully formed HTTP(S) URL. Returns page content in markdown (default), html, or text format.
Use this to read a supplied URL or inspect a search result beyond its snippet.
Network access, authentication, redirects, and JavaScript rendering depend on the configured fetcher.`

func (*FetchTool) ConcurrencyPolicy() func(toolcontract.Invocation) (string, bool) {
	return func(toolcontract.Invocation) (string, bool) { return "", true }
}
