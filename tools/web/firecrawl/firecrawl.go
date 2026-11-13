package firecrawl

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"unicode/utf8"

	"github.com/Tangerg/scope/tools/web/internal/googletbs"

	"github.com/go-resty/resty/v2"

	"github.com/Tangerg/scope/tools/internal/toolresult"
	"github.com/Tangerg/scope/tools/web"
	"github.com/Tangerg/scope/tools/web/internal/providerhttp"
)

const (
	baseURL                  = "https://api.firecrawl.dev/v2"
	searchPath               = "/search"
	scrapePath               = "/scrape"
	defaultSearchResultCount = 10
	maxQueryCharacters       = 500
	scrapeFormatMarkdown     = "markdown"
	scrapeFormatHTML         = "html"
)

// Config configures a [Client]. APIKey is required; an empty BaseURL selects
// Firecrawl's public endpoint and a nil HTTPClient selects a default client.
type Config struct {
	APIKey  string
	BaseURL string
	// HTTPClient remains host-owned. Requests and redirects are restricted to
	// BaseURL's origin, including when the host redirect policy changes a request.
	HTTPClient *http.Client
}

// Client implements [web.Searcher] and [web.Fetcher] against Firecrawl, whose
// fetch path renders a page before extracting content produced by client-side
// scripts.
type Client struct {
	http *resty.Client
}

var _ web.Searcher = (*Client)(nil)

func NewClient(ctx context.Context, config Config) (*Client, error) {
	if config.APIKey == "" {
		return nil, errors.New("firecrawl: API key is required")
	}
	transport, err := providerhttp.NewClient(ctx, providerhttp.Config{HTTPClient: config.HTTPClient, BaseURL: cmp.Or(config.BaseURL, baseURL)})
	if err != nil {
		return nil, fmt.Errorf("firecrawl: %w", err)
	}
	transport.SetAuthToken(config.APIKey)
	return &Client{http: transport}, nil
}

type searchRequest struct {
	Query          string   `json:"query"`
	Limit          int      `json:"limit,omitzero"`
	Tbs            string   `json:"tbs,omitempty"`
	IncludeDomains []string `json:"includeDomains,omitempty"`
	ExcludeDomains []string `json:"excludeDomains,omitempty"`
}

func newSearchRequest(request *web.SearchRequest) (*searchRequest, error) {
	if utf8.RuneCountInString(request.Query) > maxQueryCharacters {
		return nil, fmt.Errorf("firecrawl: %w: query exceeds %d characters", web.ErrInvalidQuery, maxQueryCharacters)
	}
	return &searchRequest{
		Query:          request.Query,
		Limit:          cmp.Or(request.MaxResults, defaultSearchResultCount),
		Tbs:            googletbs.FromRecency(request.Recency),
		IncludeDomains: request.AllowedDomains,
		ExcludeDomains: request.BlockedDomains,
	}, nil
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
	Success bool                `json:"success"`
	Data    *searchResponseData `json:"data"`
}

func (s *searchResponse) toSearchResponse(request *web.SearchRequest) (*web.SearchResponse, error) {
	if s.Data == nil {
		return nil, fmt.Errorf("firecrawl: %w: response is missing data", web.ErrInvalidSearchResponse)
	}
	results := make([]*web.SearchResult, 0, len(s.Data.Web))
	for index, searchResult := range s.Data.Web {
		if searchResult == nil {
			return nil, fmt.Errorf("firecrawl: %w: result %d is null", web.ErrInvalidSearchResponse, index)
		}
		results = append(results, &web.SearchResult{
			Title:   searchResult.Title,
			URL:     searchResult.URL,
			Snippet: searchResult.Description,
		})
	}
	response := &web.SearchResponse{Query: request.Query, Results: results}
	if err := response.ValidateFor(request); err != nil {
		return nil, fmt.Errorf("firecrawl: %w", err)
	}
	return response, nil
}

func (c *Client) Search(ctx context.Context, request *web.SearchRequest) (*web.SearchResponse, error) {
	prepared, err := request.Prepare()
	if err != nil {
		cause := fmt.Errorf("firecrawl: prepare search request: %w", err)
		return nil, toolresult.Correctable(cause.Error(), cause)
	}
	body, err := newSearchRequest(prepared)
	if err != nil {
		return nil, toolresult.Correctable(err.Error(), err)
	}
	var raw searchResponse
	httpRequest := c.http.R().SetContext(ctx).SetBody(body)
	err = providerhttp.Execute(httpRequest, http.MethodPost, searchPath, &raw)
	if err != nil {
		return nil, fmt.Errorf("firecrawl: search request: %w", err)
	}
	if !raw.Success {
		return nil, errors.New("firecrawl: search response reported failure")
	}
	return raw.toSearchResponse(prepared)
}

func (c *Client) Fetch(ctx context.Context, request *web.FetchRequest) (*web.FetchResponse, error) {
	prepared, err := request.Prepare()
	if err != nil {
		cause := fmt.Errorf("firecrawl: prepare fetch request: %w", err)
		return nil, toolresult.Correctable(cause.Error(), cause)
	}
	format := prepared.Format
	scrapeFormat, supported := contentScrapeFormat(format)
	if !supported {
		cause := fmt.Errorf("firecrawl: %w: %s", web.ErrUnsupportedFormat, format)
		return nil, toolresult.Correctable(cause.Error(), cause)
	}
	var raw fetchResponse
	httpRequest := c.http.R().SetContext(ctx).SetBody(&fetchRequest{
		URL:             prepared.URL,
		Formats:         []fetchFormat{{Type: scrapeFormat}},
		OnlyMainContent: true,
	})
	err = providerhttp.Execute(httpRequest, http.MethodPost, scrapePath, &raw)
	if err != nil {
		return nil, fmt.Errorf("firecrawl: fetch request: %w", err)
	}
	if !raw.Success {
		return nil, errors.New("firecrawl: fetch response reported failure")
	}
	content := raw.Data.content(format)
	if content == nil {
		return nil, fmt.Errorf("firecrawl: response is missing requested %s content", format)
	}
	result := &web.FetchResponse{Content: *content, Format: format}
	if err := result.ValidateFor(prepared); err != nil {
		return nil, fmt.Errorf("firecrawl: %w", err)
	}
	return result, nil
}

// contentScrapeFormat maps a content format onto Firecrawl's scrape format,
// which has no plain-text rendering.
func contentScrapeFormat(format web.ContentFormat) (string, bool) {
	switch format {
	case web.FormatMarkdown:
		return scrapeFormatMarkdown, true
	case web.FormatHTML:
		return scrapeFormatHTML, true
	default:
		return "", false
	}
}
