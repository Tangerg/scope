package web

import (
	"context"
	"errors"
	"fmt"

	"github.com/samber/lo"

	toolcontract "github.com/Tangerg/scope/core/tool"
	"github.com/Tangerg/scope/tools/internal/toolresult"
)

var _ toolcontract.Tool = (*SearchTool)(nil)

type SearchTool struct {
	toolcontract.Func[SearchRequest, *SearchResponse]
	searcher Searcher
}

func NewSearchTool(searcher Searcher) (*SearchTool, error) {
	if lo.IsNil(searcher) {
		return nil, ErrMissingSearcher
	}
	s := &SearchTool{searcher: searcher}
	typed, err := toolcontract.NewFunc(
		toolcontract.FuncConfig{Effect: toolcontract.EffectSafe, Name: "web_search", Description: webSearchDescription},
		s.search,
	)
	if err != nil {
		return nil, fmt.Errorf("web: build search tool: %w", err)
	}
	s.Func = typed
	return s, nil
}

func (s *SearchTool) search(ctx context.Context, request SearchRequest) (*SearchResponse, error) {
	prepared, err := request.Prepare()
	if err != nil {
		return nil, toolresult.Correctable(err.Error(), err)
	}
	response, err := s.searcher.Search(ctx, prepared)
	if err != nil {
		// An unsupported filter is rejected before any backend call: definite, not uncertain.
		if errors.Is(err, ErrUnsupportedFilter) {
			return nil, toolresult.Correctable(err.Error(), err)
		}
		return nil, fmt.Errorf("web.search: %w", err)
	}
	if validationErr := response.ValidateFor(prepared); validationErr != nil {
		return nil, fmt.Errorf("web: validate search response: %w", validationErr)
	}
	return response, nil
}

const webSearchDescription = `Search the web for current information. Results include titles, URLs, and snippets.
Use max_results to bound the response. allowed_domains and blocked_domains are mutually exclusive.
Use recency for a relative freshness window. Cite the returned source URLs when using their content.`

func (*SearchTool) ConcurrencyPolicy() func(toolcontract.Invocation) (string, bool) {
	return func(toolcontract.Invocation) (string, bool) { return "", true }
}
