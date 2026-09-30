package tavily

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
	baseURL                  = "https://api.tavily.com"
	searchPath               = "/search"
	extractPath              = "/extract"
	depthBasic               = "basic"
	topicGeneral             = "general"
	defaultSearchResultCount = 5
)

// Config configures a [Client]. APIKey is required; an empty BaseURL selects
// Tavily's public endpoint and a nil HTTPClient selects a default client.
type Config struct {
	APIKey     string
	BaseURL    string
	HTTPClient *http.Client
}

// Client implements [web.Searcher] and [web.Fetcher] against Tavily Search and
// Extract.
type Client struct {
	http *resty.Client
}

var _ web.Searcher = (*Client)(nil)

func NewClient(config Config) (*Client, error) {
	if config.APIKey == "" {
		return nil, errors.New("tavily: API key is required")
	}
	transport := providerhttp.NewClient(config.HTTPClient, cmp.Or(config.BaseURL, baseURL)).
		SetAuthToken(config.APIKey).
		SetHeader("Content-Type", "application/json")
	return &Client{http: transport}, nil
}

type searchRequest struct {
	Query          string   `json:"query"`
	SearchDepth    string   `json:"search_depth,omitempty"`
	Topic          string   `json:"topic,omitempty"`
	MaxResults     int      `json:"max_results,omitzero"`
	TimeRange      string   `json:"time_range,omitempty"`
	IncludeDomains []string `json:"include_domains,omitempty"`
	ExcludeDomains []string `json:"exclude_domains,omitempty"`
	IncludeFavicon bool     `json:"include_favicon,omitzero"`
}

func newSearchRequest(request *web.SearchRequest) *searchRequest {
	return &searchRequest{
		Query:          request.Query,
		SearchDepth:    depthBasic,
		Topic:          topicGeneral,
		MaxResults:     cmp.Or(request.MaxResults, defaultSearchResultCount),
		TimeRange:      recencyToTimeRange(request.Recency),
		IncludeDomains: request.AllowedDomains,
		ExcludeDomains: request.BlockedDomains,
		IncludeFavicon: true,
	}
}

type searchResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Content string `json:"content"`
	Favicon string `json:"favicon,omitempty"`
}

type searchResponse struct {
	Results []*searchResult `json:"results"`
}

func (s *searchResponse) toSearchResponse(query string) *web.SearchResponse {
	results := make([]*web.SearchResult, 0, len(s.Results))
	for _, searchResult := range s.Results {
		if searchResult == nil {
			continue
		}
		results = append(results, &web.SearchResult{
			Title:      searchResult.Title,
			URL:        searchResult.URL,
			Snippet:    searchResult.Content,
			FaviconURL: searchResult.Favicon,
		})
	}
	return &web.SearchResponse{Query: query, Results: results}
}

func (c *Client) Search(ctx context.Context, request *web.SearchRequest) (*web.SearchResponse, error) {
	prepared, err := request.Prepare()
	if err != nil {
		return nil, fmt.Errorf("tavily: prepare search request: %w", err)
	}
	if prepared.Recency == web.RecencyHour {
		return nil, fmt.Errorf("tavily: %w: hourly recency", web.ErrUnsupportedFilter)
	}
	var raw searchResponse
	httpRequest := c.http.R().SetContext(ctx).SetBody(newSearchRequest(prepared))
	if _, err := providerhttp.Execute(httpRequest, http.MethodPost, searchPath, &raw); err != nil {
		return nil, fmt.Errorf("tavily: search request: %w", err)
	}
	return raw.toSearchResponse(prepared.Query), nil
}

func (c *Client) Fetch(ctx context.Context, request *web.FetchRequest) (*web.FetchResponse, error) {
	prepared, err := request.Prepare()
	if err != nil {
		return nil, fmt.Errorf("tavily: prepare fetch request: %w", err)
	}
	format := prepared.Format
	if format == web.FormatHTML {
		return nil, fmt.Errorf("tavily: %w: %s", web.ErrUnsupportedFormat, format)
	}
	var raw fetchResponse
	httpRequest := c.http.R().SetContext(ctx).SetBody(&fetchRequest{
		URLs:         []string{prepared.URL},
		ExtractDepth: depthBasic,
		Format:       string(format),
	})
	if _, err = providerhttp.Execute(httpRequest, http.MethodPost, extractPath, &raw); err != nil {
		return nil, fmt.Errorf("tavily: fetch request: %w", err)
	}
	content, err := raw.content()
	if err != nil {
		return nil, err
	}
	return &web.FetchResponse{Content: content, Format: format}, nil
}

func recencyToTimeRange(r web.Recency) string {
	switch r {
	case web.RecencyDay:
		return "day"
	case web.RecencyWeek:
		return "week"
	case web.RecencyMonth:
		return "month"
	case web.RecencyYear:
		return "year"
	}
	return ""
}
