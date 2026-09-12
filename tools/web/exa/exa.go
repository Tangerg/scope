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
)

const defaultSearchResultCount = 10

const (
	baseURL = "https://api.exa.ai"
)

// Config takes an optional [http.Client] so the host keeps ownership of
// timeouts, proxying, and transport instrumentation instead of inheriting
// whatever this package would otherwise choose.
type Config struct {
	APIKey     string
	BaseURL    string
	HTTPClient *http.Client
}

// Client implements [web.Searcher] against the Exa API. It exposes no
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
		return nil, errors.New("exa: API key is required")
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
			SetHeader("x-api-key", config.APIKey).
			SetHeader("Content-Type", "application/json"),
	}, nil
}

type summaryOptions struct {
	Query string `json:"query,omitempty"`
}

type contentsOptions struct {
	Summary *summaryOptions `json:"summary,omitempty"`
}

type searchRequest struct {
	Query              string           `json:"query"`
	Type               string           `json:"type,omitempty"`
	NumResults         int              `json:"numResults,omitempty"`
	IncludeDomains     []string         `json:"includeDomains,omitempty"`
	ExcludeDomains     []string         `json:"excludeDomains,omitempty"`
	StartPublishedDate string           `json:"startPublishedDate,omitempty"`
	Contents           *contentsOptions `json:"contents,omitempty"`
}

func (s *searchRequest) validate() error {
	if s == nil {
		return errors.New("exa: search request must not be nil")
	}
	if s.Query == "" {
		return errors.New("exa: search query must not be empty")
	}
	return nil
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

type searchResponse struct {
	Results []*searchResult `json:"results"`
}

func (c *Client) search(ctx context.Context, request *searchRequest) (*searchResponse, error) {
	if err := request.validate(); err != nil {
		return nil, err
	}
	var raw searchResponse
	response, err := c.http.R().SetContext(ctx).SetBody(request).SetResult(&raw).Post("/search")
	if err != nil {
		return nil, fmt.Errorf("exa: execute search request: %w", err)
	}
	if !response.IsSuccess() {
		return nil, fmt.Errorf("exa: search request returned HTTP %d: %s", response.StatusCode(), response.String())
	}
	return &raw, nil
}

func (c *Client) Search(ctx context.Context, request *web.SearchRequest) (*web.SearchResponse, error) {
	prepared, err := request.Prepare()
	if err != nil {
		return nil, fmt.Errorf("exa: prepare search request: %w", err)
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
	response, err := c.http.R().SetContext(ctx).SetBody(request).SetResult(&raw).Post("/contents")
	if err != nil {
		return nil, fmt.Errorf("exa: execute fetch request: %w", err)
	}
	if !response.IsSuccess() {
		return nil, fmt.Errorf("exa: fetch request returned HTTP %d: %s", response.StatusCode(), response.String())
	}
	return &raw, nil
}

func (c *Client) Fetch(ctx context.Context, request *web.FetchRequest) (*web.FetchResponse, error) {
	prepared, err := request.Prepare()
	if err != nil {
		return nil, fmt.Errorf("exa: prepare fetch request: %w", err)
	}
	request = prepared
	format := request.Format
	raw, err := c.fetch(ctx, &fetchRequest{
		URLs: []string{request.URL},
		Text: fetchTextOptions{IncludeHTMLTags: format == web.FormatHTML},
	})
	if err != nil {
		return nil, err
	}
	if len(raw.Results) == 0 || raw.Results[0] == nil {
		return nil, errors.New("exa: fetch response contains no result")
	}
	return &web.FetchResponse{Content: raw.Results[0].Text, Format: format}, nil
}

func buildSearchRequest(request *web.SearchRequest) *searchRequest {
	r := &searchRequest{
		Query:      request.Query,
		Type:       "fast",
		NumResults: cmp.Or(request.MaxResults, defaultSearchResultCount),
		Contents: &contentsOptions{
			Summary: &summaryOptions{Query: request.Query},
		},
	}
	if len(request.AllowedDomains) > 0 {
		r.IncludeDomains = request.AllowedDomains
	}
	if len(request.BlockedDomains) > 0 {
		r.ExcludeDomains = request.BlockedDomains
	}
	if start := recencyToStart(request.Recency); !start.IsZero() {
		r.StartPublishedDate = start.Format(time.RFC3339)
	}
	return r
}

func recencyToStart(r web.Recency) time.Time {
	now := time.Now()
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

func (s *searchResponse) toSearchResponse(query string) *web.SearchResponse {
	results := make([]*web.SearchResult, 0, len(s.Results))
	for _, searchResult := range s.Results {
		if searchResult == nil {
			continue
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
	return &web.SearchResponse{Query: query, Results: results}
}

func (s *searchResult) snippet() string {
	if len(s.Highlights) > 0 {
		return s.Highlights[0]
	}
	return s.Summary
}

func parseDate(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t
	}
	return time.Time{}
}
