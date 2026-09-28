package web

import (
	"context"
	"fmt"

	"github.com/samber/lo"

	toolcontract "github.com/Tangerg/scope/core/tool"
)

var _ toolcontract.Tool = (*SearchTool)(nil)

type SearchTool struct {
	readOnlyTool
	searcher Searcher
}

func NewSearchTool(searcher Searcher) (*SearchTool, error) {
	if lo.IsNil(searcher) {
		return nil, ErrMissingSearcher
	}
	s := &SearchTool{searcher: searcher}
	inner, err := toolcontract.NewFunc(
		toolcontract.FuncConfig{Name: "web_search", Description: webSearchDescription},
		s.search,
	)
	if err != nil {
		return nil, fmt.Errorf("web: build search tool: %w", err)
	}
	s.inner = inner
	return s, nil
}

func (s *SearchTool) search(ctx context.Context, request SearchRequest) (*SearchResponse, error) {
	prepared, err := request.Prepare()
	if err != nil {
		return nil, fmt.Errorf("web: prepare search request: %w", err)
	}
	response, err := s.searcher.Search(ctx, prepared)
	if err != nil {
		return nil, fmt.Errorf("web: execute search: %w", err)
	}
	if validationErr := response.Validate(); validationErr != nil {
		return nil, fmt.Errorf("web: validate search response: %w", validationErr)
	}
	return response, nil
}

const webSearchDescription = `Search the web for current information. Results include titles, URLs, and snippets.
Use max_results to bound the response. allowed_domains and blocked_domains are mutually exclusive.
Use recency for a relative freshness window. Cite the returned source URLs when using their content.`
