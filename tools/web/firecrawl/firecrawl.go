package firecrawl

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/go-resty/resty/v2"

	"github.com/Tangerg/scope/tools/web"
)

const defaultSearchResultCount = 10

const (
	baseURL = "https://api.firecrawl.dev/v2"
)

// Config takes an optional [http.Client] so the host keeps ownership of
// timeouts, proxying, and transport instrumentation instead of inheriting
// whatever this package would otherwise choose.
type Config struct {
	APIKey     string
	BaseURL    string
	HTTPClient *http.Client
}

// Client implements the web contracts against Firecrawl, whose fetch path can
// render a page before extracting content produced by client-side scripts.
type Client struct {
	http *resty.Client
}

var _ web.Searcher = (*Client)(nil)

// NewClient requires the API key at construction so a missing credential fails
// at wiring rather than as an authorization error on a model's first fetch.
func NewClient(config Config) (*Client, error) {
	if config.APIKey == "" {
		return nil, errors.New("firecrawl: API key is required")
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
	Query string `json:"query"`
	Limit int    `json:"limit,omitempty"`
	Tbs   string `json:"tbs,omitempty"`
}

func (s *searchRequest) validate() error {
	if s == nil {
		return errors.New("firecrawl: search request must not be nil")
	}
	if s.Query == "" {
		return errors.New("firecrawl: search query must not be empty")
	}
	return nil
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

func (c *Client) search(ctx context.Context, request *searchRequest) (*searchResponse, error) {
	if err := request.validate(); err != nil {
		return nil, err
	}
	var raw searchResponse
	response, err := c.http.R().SetContext(ctx).SetBody(request).SetResult(&raw).Post("/search")
	if err != nil {
		return nil, fmt.Errorf("firecrawl: execute search request: %w", err)
	}
	if !response.IsSuccess() {
		return nil, fmt.Errorf("firecrawl: search request returned HTTP %d: %s", response.StatusCode(), response.String())
	}
	if !raw.Success {
		return nil, fmt.Errorf("firecrawl: search response reported failure: %s", response.String())
	}
	return &raw, nil
}

func (c *Client) Search(ctx context.Context, request *web.SearchRequest) (*web.SearchResponse, error) {
	prepared, err := request.Prepare()
	if err != nil {
		return nil, fmt.Errorf("firecrawl: prepare search request: %w", err)
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
	response, err := c.http.R().SetContext(ctx).SetBody(request).SetResult(&raw).Post("/scrape")
	if err != nil {
		return nil, fmt.Errorf("firecrawl: execute fetch request: %w", err)
	}
	if !response.IsSuccess() {
		return nil, fmt.Errorf("firecrawl: fetch request returned HTTP %d: %s", response.StatusCode(), response.String())
	}
	if !raw.Success {
		return nil, fmt.Errorf("firecrawl: fetch response reported failure: %s", response.String())
	}
	return &raw, nil
}

func (c *Client) Fetch(ctx context.Context, request *web.FetchRequest) (*web.FetchResponse, error) {
	prepared, err := request.Prepare()
	if err != nil {
		return nil, fmt.Errorf("firecrawl: prepare fetch request: %w", err)
	}
	request = prepared
	format := request.Format
	if format == web.FormatText {
		return nil, fmt.Errorf("firecrawl: %w: %s", web.ErrUnsupportedFormat, format)
	}
	raw, err := c.fetch(ctx, &fetchRequest{
		URL:             request.URL,
		Formats:         []fetchFormat{{Type: string(format)}},
		OnlyMainContent: true,
	})
	if err != nil {
		return nil, err
	}
	content := raw.Data.Markdown
	if format == web.FormatHTML && raw.Data.HTML != nil {
		content = *raw.Data.HTML
	}
	return &web.FetchResponse{Content: content, Format: format}, nil
}

func buildSearchRequest(request *web.SearchRequest) *searchRequest {
	r := &searchRequest{
		Query: request.QueryWithSiteOperators(),
		Limit: cmp.Or(request.MaxResults, defaultSearchResultCount),
	}
	r.Tbs = recencyToTbs(request.Recency)
	return r
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
