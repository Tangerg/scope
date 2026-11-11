package web

import (
	"context"
	"fmt"

	"github.com/samber/lo"

	toolcontract "github.com/Tangerg/scope/core/tool"
)

const webSearchToolName = "web_search"

var _ toolcontract.Tool = (*SearchTool)(nil)

// searchFunc is embedded under an unexported name so the tool keeps its
// Func private while promoting the Tool methods.
type searchFunc = toolcontract.Func[SearchRequest, *SearchResponse]

type SearchTool struct {
	searchFunc
	searcher Searcher
}

func NewSearchTool(searcher Searcher) (*SearchTool, error) {
	if lo.IsNil(searcher) {
		return nil, ErrMissingSearcher
	}
	s := &SearchTool{searcher: searcher}
	typed, err := toolcontract.NewFunc(
		toolcontract.FuncConfig{Effect: toolcontract.EffectSafe, Name: webSearchToolName, Description: webSearchDescription},
		s.search,
	)
	if err != nil {
		return nil, fmt.Errorf("web: build search tool: %w", err)
	}
	s.searchFunc = typed
	return s, nil
}

func (s *SearchTool) search(ctx context.Context, request SearchRequest) (*SearchResponse, error) {
	return invokePort[SearchRequest](ctx, &request, "search", s.searcher.Search)
}

const webSearchDescription = `Search the web for current information. Results include titles, URLs, and snippets.
Use max_results to bound the response. allowed_domains and blocked_domains are mutually exclusive.
Use recency for a relative freshness window. Cite the returned source URLs when using their content.`

func (*SearchTool) ConcurrencyPolicy() func(toolcontract.Invocation) (string, bool) {
	return func(toolcontract.Invocation) (string, bool) { return "", true }
}
