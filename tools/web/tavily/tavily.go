package tavily

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"unicode/utf8"

	"github.com/go-resty/resty/v2"

	"github.com/Tangerg/scope/tools/internal/toolresult"
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
	maximumQueryCharacters   = 400
	timeRangeDay             = "day"
	timeRangeWeek            = "week"
	timeRangeMonth           = "month"
	timeRangeYear            = "year"
	extractFormatMarkdown    = "markdown"
	extractFormatText        = "text"
)

// Config configures a [Client]. APIKey is required; an empty BaseURL selects
// Tavily's public endpoint and a nil HTTPClient selects a default client.
type Config struct {
	APIKey  string
	BaseURL string
	// HTTPClient remains host-owned. Requests and redirects are restricted to
	// BaseURL's origin, including when the host redirect policy changes a request.
	HTTPClient *http.Client
}

type Client struct {
	http *resty.Client
}

var _ web.Searcher = (*Client)(nil)

func NewClient(ctx context.Context, config Config) (*Client, error) {
	if config.APIKey == "" {
		return nil, errors.New("tavily: API key is required")
	}
	transport, err := providerhttp.NewClient(ctx, providerhttp.Config{HTTPClient: config.HTTPClient, BaseURL: cmp.Or(config.BaseURL, baseURL)})
	if err != nil {
		return nil, fmt.Errorf("tavily: %w", err)
	}
	transport.SetAuthToken(config.APIKey)
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

func newSearchRequest(request *web.SearchRequest) (*searchRequest, error) {
	if utf8.RuneCountInString(request.Query) > maximumQueryCharacters {
		return nil, fmt.Errorf("tavily: %w: query must contain at most %d characters", web.ErrInvalidQuery, maximumQueryCharacters)
	}
	timeRange, supported := recencyTimeRange(request.Recency)
	if !supported {
		return nil, fmt.Errorf("tavily: %w: %s recency", web.ErrUnsupportedFilter, request.Recency)
	}
	return &searchRequest{
		Query:          request.Query,
		SearchDepth:    depthBasic,
		Topic:          topicGeneral,
		MaxResults:     cmp.Or(request.MaxResults, defaultSearchResultCount),
		TimeRange:      timeRange,
		IncludeDomains: request.AllowedDomains,
		ExcludeDomains: request.BlockedDomains,
		IncludeFavicon: true,
	}, nil
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

func (s *searchResponse) toSearchResponse(request *web.SearchRequest) (*web.SearchResponse, error) {
	if s.Results == nil {
		return nil, fmt.Errorf("tavily: %w: response is missing results", web.ErrInvalidSearchResponse)
	}
	results := make([]*web.SearchResult, 0, len(s.Results))
	for index, searchResult := range s.Results {
		if searchResult == nil {
			return nil, fmt.Errorf("tavily: %w: result %d is null", web.ErrInvalidSearchResponse, index)
		}
		results = append(results, &web.SearchResult{
			Title:      searchResult.Title,
			URL:        searchResult.URL,
			Snippet:    searchResult.Content,
			FaviconURL: searchResult.Favicon,
		})
	}
	response := &web.SearchResponse{Query: request.Query, Results: results}
	if err := response.ValidateFor(request); err != nil {
		return nil, fmt.Errorf("tavily: %w", err)
	}
	return response, nil
}

func (c *Client) Search(ctx context.Context, request *web.SearchRequest) (*web.SearchResponse, error) {
	prepared, err := request.Prepare()
	if err != nil {
		cause := fmt.Errorf("tavily: prepare search request: %w", err)
		return nil, toolresult.Correctable(cause.Error(), cause)
	}
	native, err := newSearchRequest(prepared)
	if err != nil {
		return nil, toolresult.Correctable(err.Error(), err)
	}
	var raw searchResponse
	httpRequest := c.http.R().SetContext(ctx).SetBody(native)
	if err := providerhttp.Execute(httpRequest, http.MethodPost, searchPath, &raw); err != nil {
		return nil, fmt.Errorf("tavily: search request: %w", err)
	}
	return raw.toSearchResponse(prepared)
}

func (c *Client) Fetch(ctx context.Context, request *web.FetchRequest) (*web.FetchResponse, error) {
	prepared, err := request.Prepare()
	if err != nil {
		cause := fmt.Errorf("tavily: prepare fetch request: %w", err)
		return nil, toolresult.Correctable(cause.Error(), cause)
	}
	format := prepared.Format
	extractFormat, supported := contentExtractFormat(format)
	if !supported {
		cause := fmt.Errorf("tavily: %w: %s", web.ErrUnsupportedFormat, format)
		return nil, toolresult.Correctable(cause.Error(), cause)
	}
	var raw fetchResponse
	httpRequest := c.http.R().SetContext(ctx).SetBody(&fetchRequest{
		URLs:         []string{prepared.URL},
		ExtractDepth: depthBasic,
		Format:       extractFormat,
	})
	if err = providerhttp.Execute(httpRequest, http.MethodPost, extractPath, &raw); err != nil {
		return nil, fmt.Errorf("tavily: fetch request: %w", err)
	}
	content, err := raw.content(prepared.URL)
	if err != nil {
		return nil, err
	}
	result := &web.FetchResponse{Content: content, Format: format}
	if err := result.ValidateFor(prepared); err != nil {
		return nil, fmt.Errorf("tavily: %w", err)
	}
	return result, nil
}

// recencyTimeRange maps a recency onto Tavily's time range. Tavily has no
// hourly range, and an unset recency sends none.
func recencyTimeRange(recency web.Recency) (string, bool) {
	switch recency {
	case "":
		return "", true
	case web.RecencyDay:
		return timeRangeDay, true
	case web.RecencyWeek:
		return timeRangeWeek, true
	case web.RecencyMonth:
		return timeRangeMonth, true
	case web.RecencyYear:
		return timeRangeYear, true
	default:
		return "", false
	}
}

// contentExtractFormat maps a content format onto Tavily's extract format,
// which has no HTML rendering.
func contentExtractFormat(format web.ContentFormat) (string, bool) {
	switch format {
	case web.FormatMarkdown:
		return extractFormatMarkdown, true
	case web.FormatText:
		return extractFormatText, true
	default:
		return "", false
	}
}
