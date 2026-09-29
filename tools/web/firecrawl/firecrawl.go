package firecrawl

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/go-resty/resty/v2"

	"github.com/Tangerg/scope/tools/web"
	"github.com/Tangerg/scope/tools/web/internal/providerhttp"
)

const (
	baseURL                  = "https://api.firecrawl.dev/v2"
	searchPath               = "/search"
	scrapePath               = "/scrape"
	defaultSearchResultCount = 10
)

// Config configures a [Client]. APIKey is required; an empty BaseURL selects
// Firecrawl's public endpoint and a nil HTTPClient selects a default client.
type Config struct {
	APIKey     string
	BaseURL    string
	HTTPClient *http.Client
}

// Client implements [web.Searcher] and [web.Fetcher] against Firecrawl, whose
// fetch path renders a page before extracting content produced by client-side
// scripts.
type Client struct {
	http *resty.Client
}

var _ web.Searcher = (*Client)(nil)

func NewClient(config Config) (*Client, error) {
	if config.APIKey == "" {
		return nil, errors.New("firecrawl: API key is required")
	}
	transport := providerhttp.NewClient(config.HTTPClient, cmp.Or(config.BaseURL, baseURL)).
		SetAuthToken(config.APIKey).
		SetHeader("Content-Type", "application/json")
	return &Client{http: transport}, nil
}

type searchRequest struct {
	Query string `json:"query"`
	Limit int    `json:"limit,omitzero"`
	Tbs   string `json:"tbs,omitempty"`
}

func newSearchRequest(request *web.SearchRequest) *searchRequest {
	return &searchRequest{
		Query: request.QueryWithSiteOperators(),
		Limit: cmp.Or(request.MaxResults, defaultSearchResultCount),
		Tbs:   recencyToTbs(request.Recency),
	}
}

type searchResult struct {
	Title       string `json:"title"`
	URL         string `json:"url"`
	Description string `json:"description"`
}

type searchResponseData struct {
	Web []*searchResult `json:"web,omitempty"`
}

type searchResponse struct {
	Success bool               `json:"success"`
	Data    searchResponseData `json:"data"`
}

func (s *searchResponse) toSearchResponse(query string) *web.SearchResponse {
	results := make([]*web.SearchResult, 0, len(s.Data.Web))
	for _, searchResult := range s.Data.Web {
		if searchResult == nil {
			continue
		}
		results = append(results, &web.SearchResult{
			Title:   searchResult.Title,
			URL:     searchResult.URL,
			Snippet: searchResult.Description,
		})
	}
	return &web.SearchResponse{Query: query, Results: results}
}

func (c *Client) Search(ctx context.Context, request *web.SearchRequest) (*web.SearchResponse, error) {
	prepared, err := request.Prepare()
	if err != nil {
		return nil, fmt.Errorf("firecrawl: prepare search request: %w", err)
	}
	var raw searchResponse
	httpRequest := c.http.R().SetContext(ctx).SetBody(newSearchRequest(prepared))
	response, err := providerhttp.Execute(httpRequest, http.MethodPost, searchPath, &raw)
	if err != nil {
		return nil, fmt.Errorf("firecrawl: search request: %w", err)
	}
	if !raw.Success {
		return nil, fmt.Errorf("firecrawl: search response reported failure: %s", response.String())
	}
	return raw.toSearchResponse(prepared.Query), nil
}

func (c *Client) Fetch(ctx context.Context, request *web.FetchRequest) (*web.FetchResponse, error) {
	prepared, err := request.Prepare()
	if err != nil {
		return nil, fmt.Errorf("firecrawl: prepare fetch request: %w", err)
	}
	format := prepared.Format
	if format == web.FormatText {
		return nil, fmt.Errorf("firecrawl: %w: %s", web.ErrUnsupportedFormat, format)
	}
	var raw fetchResponse
	httpRequest := c.http.R().SetContext(ctx).SetBody(&fetchRequest{
		URL:             prepared.URL,
		Formats:         []fetchFormat{{Type: string(format)}},
		OnlyMainContent: true,
	})
	response, err := providerhttp.Execute(httpRequest, http.MethodPost, scrapePath, &raw)
	if err != nil {
		return nil, fmt.Errorf("firecrawl: fetch request: %w", err)
	}
	if !raw.Success {
		return nil, fmt.Errorf("firecrawl: fetch response reported failure: %s", response.String())
	}
	content := raw.Data.content(format)
	if content == nil {
		return nil, fmt.Errorf("firecrawl: response is missing requested %s content", format)
	}
	return &web.FetchResponse{Content: *content, Format: format}, nil
}

func recencyToTbs(r web.Recency) string {
	switch r {
	case web.RecencyHour:
		return "qdr:h"
	case web.RecencyDay:
		return "qdr:d"
	case web.RecencyWeek:
		return "qdr:w"
	case web.RecencyMonth:
		return "qdr:m"
	case web.RecencyYear:
		return "qdr:y"
	}
	return ""
}
