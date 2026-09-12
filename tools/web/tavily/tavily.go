package tavily

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/go-resty/resty/v2"

	"github.com/Tangerg/scope/tools/web"
)

const defaultSearchResultCount = 5

const (
	baseURL = "https://api.tavily.com"
)

// Config takes an optional [http.Client] so the host keeps ownership of
// timeouts, proxying, and transport instrumentation instead of inheriting
// whatever this package would otherwise choose.
type Config struct {
	APIKey     string
	BaseURL    string
	HTTPClient *http.Client
}

// Client implements [web.Searcher] against the Tavily API. It exposes no
// vendor-specific surface, so a caller can substitute another search backend
// without touching the tool that consumes it.
type Client struct {
	http *resty.Client
}

var _ web.Searcher = (*Client)(nil)

// NewClient requires the API key at construction so a missing credential fails
// at wiring rather than as an authorization error on a model's first search.
func NewClient(config Config) (*Client, error) {
	if config.APIKey == "" {
		return nil, errors.New("tavily: API key is required")
	}
	if config.BaseURL == "" {
		config.BaseURL = baseURL
	}
	if config.HTTPClient == nil {
		config.HTTPClient = &http.Client{}
	}
	return &Client{
		http: resty.NewWithClient(config.HTTPClient).
			SetBaseURL(config.BaseURL).
			SetAuthToken(config.APIKey).
			SetHeader("Content-Type", "application/json"),
	}, nil
}

type searchRequest struct {
	Query          string   `json:"query"`
	SearchDepth    string   `json:"search_depth,omitempty"`
	Topic          string   `json:"topic,omitempty"`
	MaxResults     int      `json:"max_results,omitempty"`
	TimeRange      string   `json:"time_range,omitempty"`
	IncludeDomains []string `json:"include_domains,omitempty"`
	ExcludeDomains []string `json:"exclude_domains,omitempty"`
	IncludeFavicon bool     `json:"include_favicon,omitempty"`
}

func (s *searchRequest) validate() error {
	if s == nil {
		return errors.New("tavily: search request must not be nil")
	}
	if s.Query == "" {
		return errors.New("tavily: search query must not be empty")
	}
	return nil
}

type searchResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Content string `json:"content"`
	Favicon string `json:"favicon,omitempty"`
}

type searchResponse struct {
	Query   string          `json:"query"`
	Results []*searchResult `json:"results"`
}

func (c *Client) search(ctx context.Context, request *searchRequest) (*searchResponse, error) {
	if err := request.validate(); err != nil {
		return nil, err
	}
	var raw searchResponse
	response, err := c.http.R().SetContext(ctx).SetBody(request).SetResult(&raw).Post("/search")
	if err != nil {
		return nil, fmt.Errorf("tavily: execute search request: %w", err)
	}
	if !response.IsSuccess() {
		return nil, fmt.Errorf("tavily: search request returned HTTP %d: %s", response.StatusCode(), response.String())
	}
	return &raw, nil
}

func (c *Client) Search(ctx context.Context, request *web.SearchRequest) (*web.SearchResponse, error) {
	prepared, err := request.Prepare()
	if err != nil {
		return nil, fmt.Errorf("tavily: prepare search request: %w", err)
	}
	request = prepared
	raw, err := c.search(ctx, buildSearchRequest(request))
	if err != nil {
		return nil, err
	}
	return raw.toSearchResponse(request.Query), nil
}

func (c *Client) fetch(ctx context.Context, request *fetchRequest) (*fetchResponse, error) {
	if err := request.validate(); err != nil {
		return nil, err
	}
	var raw fetchResponse
	response, err := c.http.R().SetContext(ctx).SetBody(request).SetResult(&raw).Post("/extract")
	if err != nil {
		return nil, fmt.Errorf("tavily: execute fetch request: %w", err)
	}
	if !response.IsSuccess() {
		return nil, fmt.Errorf("tavily: fetch request returned HTTP %d: %s", response.StatusCode(), response.String())
	}
	return &raw, nil
}

func (c *Client) Fetch(ctx context.Context, request *web.FetchRequest) (*web.FetchResponse, error) {
	prepared, err := request.Prepare()
	if err != nil {
		return nil, fmt.Errorf("tavily: prepare fetch request: %w", err)
	}
	request = prepared
	format := request.Format
	if format == web.FormatHTML {
		return nil, fmt.Errorf("tavily: %w: %s", web.ErrUnsupportedFormat, format)
	}
	raw, err := c.fetch(ctx, &fetchRequest{
		URLs:         []string{request.URL},
		ExtractDepth: "basic",
		Format:       string(format),
	})
	if err != nil {
		return nil, err
	}
	if len(raw.Results) == 0 || raw.Results[0] == nil {
		if len(raw.FailedResults) > 0 {
			if failure := raw.FailedResults[0]; failure != nil {
				return nil, fmt.Errorf("tavily: fetch response reported failure: %s", failure.Error)
			}
		}
		return nil, errors.New("tavily: fetch response contains no result")
	}
	return &web.FetchResponse{Content: raw.Results[0].RawContent, Format: format}, nil
}

func buildSearchRequest(request *web.SearchRequest) *searchRequest {
	r := &searchRequest{
		Query:          request.Query,
		SearchDepth:    "basic",
		Topic:          "general",
		MaxResults:     cmp.Or(request.MaxResults, defaultSearchResultCount),
		IncludeFavicon: true,
	}
	if len(request.AllowedDomains) > 0 {
		r.IncludeDomains = request.AllowedDomains
	}
	if len(request.BlockedDomains) > 0 {
		r.ExcludeDomains = request.BlockedDomains
	}
	r.TimeRange = recencyToTimeRange(request.Recency)
	return r
}

func recencyToTimeRange(r web.Recency) string {
	switch r {
	case web.RecencyHour, web.RecencyDay:
		return "day" // Tavily's minimum granularity
	case web.RecencyWeek:
		return "week"
	case web.RecencyMonth:
		return "month"
	case web.RecencyYear:
		return "year"
	}
	return ""
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
	return &web.SearchResponse{Query: cmp.Or(s.Query, query), Results: results}
}
