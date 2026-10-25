package exa

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/go-resty/resty/v2"

	"github.com/Tangerg/scope/tools/web"
	"github.com/Tangerg/scope/tools/web/internal/providerhttp"
)

const (
	baseURL                  = "https://api.exa.ai"
	searchPath               = "/search"
	contentsPath             = "/contents"
	apiKeyHeader             = "x-api-key"
	searchTypeFast           = "fast"
	defaultSearchResultCount = 10
)

// Config configures a [Client]. APIKey is required; an empty BaseURL selects
// Exa's public endpoint and a nil HTTPClient selects a default client.
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
		return nil, errors.New("exa: API key is required")
	}
	transport, err := providerhttp.NewClient(ctx, providerhttp.Config{HTTPClient: config.HTTPClient, BaseURL: cmp.Or(config.BaseURL, baseURL)})
	if err != nil {
		return nil, fmt.Errorf("exa: %w", err)
	}
	transport.SetHeader(apiKeyHeader, config.APIKey)
	return &Client{http: transport}, nil
}

type summaryOptions struct {
	Query string `json:"query,omitempty"`
}

type contentsOptions struct {
	Summary *summaryOptions `json:"summary,omitzero"`
}

type searchRequest struct {
	Query              string           `json:"query"`
	Type               string           `json:"type,omitempty"`
	NumResults         int              `json:"numResults,omitzero"`
	IncludeDomains     []string         `json:"includeDomains,omitempty"`
	ExcludeDomains     []string         `json:"excludeDomains,omitempty"`
	StartPublishedDate string           `json:"startPublishedDate,omitempty"`
	Contents           *contentsOptions `json:"contents,omitzero"`
}

func newSearchRequest(request *web.SearchRequest, now time.Time) *searchRequest {
	r := &searchRequest{
		Query:          request.Query,
		Type:           searchTypeFast,
		NumResults:     cmp.Or(request.MaxResults, defaultSearchResultCount),
		IncludeDomains: request.AllowedDomains,
		ExcludeDomains: request.BlockedDomains,
		Contents: &contentsOptions{
			Summary: &summaryOptions{Query: request.Query},
		},
	}
	if start := recencyToStart(request.Recency, now); !start.IsZero() {
		r.StartPublishedDate = start.Format(time.RFC3339)
	}
	return r
}

type searchResult struct {
	Title         string   `json:"title"`
	URL           string   `json:"url"`
	PublishedDate string   `json:"publishedDate,omitempty"`
	Author        string   `json:"author,omitempty"`
	Favicon       string   `json:"favicon,omitempty"`
	Highlights    []string `json:"highlights,omitempty"`
	Summary       string   `json:"summary,omitempty"`
}

func (s *searchResult) snippet() string {
	if len(s.Highlights) > 0 {
		return s.Highlights[0]
	}
	return s.Summary
}

type searchResponse struct {
	Results []*searchResult `json:"results"`
}

func (s *searchResponse) toSearchResponse(request *web.SearchRequest) (*web.SearchResponse, error) {
	if s.Results == nil {
		return nil, fmt.Errorf("exa: %w: response is missing results", web.ErrInvalidSearchResponse)
	}
	results := make([]*web.SearchResult, 0, len(s.Results))
	for index, searchResult := range s.Results {
		if searchResult == nil {
			return nil, fmt.Errorf("exa: %w: result %d is null", web.ErrInvalidSearchResponse, index)
		}
		results = append(results, &web.SearchResult{
			Title:         searchResult.Title,
			URL:           searchResult.URL,
			Snippet:       searchResult.snippet(),
			FaviconURL:    searchResult.Favicon,
			Source:        searchResult.Author,
			PublishedTime: parseDate(searchResult.PublishedDate),
		})
	}
	response := &web.SearchResponse{Query: request.Query, Results: results}
	if err := response.ValidateFor(request); err != nil {
		return nil, fmt.Errorf("exa: %w", err)
	}
	return response, nil
}

func (c *Client) Search(ctx context.Context, request *web.SearchRequest) (*web.SearchResponse, error) {
	prepared, err := request.Prepare()
	if err != nil {
		return nil, fmt.Errorf("exa: prepare search request: %w", err)
	}
	var raw searchResponse
	httpRequest := c.http.R().SetContext(ctx).SetBody(newSearchRequest(prepared, time.Now()))
	if _, err := providerhttp.Execute(httpRequest, http.MethodPost, searchPath, &raw); err != nil {
		return nil, fmt.Errorf("exa: search request: %w", err)
	}
	return raw.toSearchResponse(prepared)
}

func (c *Client) Fetch(ctx context.Context, request *web.FetchRequest) (*web.FetchResponse, error) {
	prepared, err := request.Prepare()
	if err != nil {
		return nil, fmt.Errorf("exa: prepare fetch request: %w", err)
	}
	format := prepared.Format
	if format == web.FormatText {
		return nil, fmt.Errorf("exa: %w: plain text is not available; use markdown or html", web.ErrUnsupportedFormat)
	}
	var raw fetchResponse
	httpRequest := c.http.R().SetContext(ctx).SetBody(&fetchRequest{
		URLs: []string{prepared.URL},
		Text: fetchTextOptions{IncludeHTMLTags: format == web.FormatHTML},
	})
	if _, executeErr := providerhttp.Execute(httpRequest, http.MethodPost, contentsPath, &raw); executeErr != nil {
		return nil, fmt.Errorf("exa: fetch request: %w", executeErr)
	}
	content, err := raw.content(prepared.URL)
	if err != nil {
		return nil, err
	}
	return &web.FetchResponse{Content: content, Format: format}, nil
}

func recencyToStart(r web.Recency, now time.Time) time.Time {
	switch r {
	case web.RecencyHour:
		return now.Add(-time.Hour)
	case web.RecencyDay:
		return now.Add(-24 * time.Hour)
	case web.RecencyWeek:
		return now.Add(-7 * 24 * time.Hour)
	case web.RecencyMonth:
		return now.AddDate(0, -1, 0)
	case web.RecencyYear:
		return now.AddDate(-1, 0, 0)
	}
	return time.Time{}
}

func parseDate(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}
