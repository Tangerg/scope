package serper

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/go-resty/resty/v2"

	"github.com/Tangerg/scope/tools/web"
)

const (
	baseURL = "https://google.serper.dev"
)

// Config takes an optional [http.Client] so the host keeps ownership of
// timeouts, proxying, and transport instrumentation instead of inheriting
// whatever this package would otherwise choose.
type Config struct {
	APIKey     string
	BaseURL    string
	HTTPClient *http.Client
}

// Client implements [web.Searcher] against the Serper API. It exposes no
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
		return nil, errors.New("serper: API key is required")
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
			SetHeader("X-API-KEY", config.APIKey).
			SetHeader("Content-Type", "application/json"),
	}, nil
}

type searchRequest struct {
	Q           string `json:"q"`
	Num         int    `json:"num,omitzero"`
	Autocorrect bool   `json:"autocorrect,omitzero"`
	Tbs         string `json:"tbs,omitempty"`
}

func (s *searchRequest) validate() error {
	if s == nil {
		return errors.New("serper: search request must not be nil")
	}
	if s.Q == "" {
		return errors.New("serper: search query must not be empty")
	}
	return nil
}

type searchParameters struct {
	Q string `json:"q"`
}

type organicResult struct {
	Title   string `json:"title"`
	Link    string `json:"link"`
	Snippet string `json:"snippet"`
	Date    string `json:"date,omitempty"`
}

type searchResponse struct {
	SearchParameters searchParameters `json:"searchParameters"`
	Organic          []*organicResult `json:"organic"`
}

func (c *Client) search(ctx context.Context, request *searchRequest) (*searchResponse, error) {
	if err := request.validate(); err != nil {
		return nil, err
	}
	var raw searchResponse
	response, err := c.http.R().SetContext(ctx).SetBody(request).SetResult(&raw).Post("/search")
	if err != nil {
		return nil, fmt.Errorf("serper: execute search request: %w", err)
	}
	if !response.IsSuccess() {
		return nil, fmt.Errorf("serper: search request returned HTTP %d: %s", response.StatusCode(), response.String())
	}
	return &raw, nil
}

func (c *Client) Search(ctx context.Context, request *web.SearchRequest) (*web.SearchResponse, error) {
	prepared, err := request.Prepare()
	if err != nil {
		return nil, fmt.Errorf("serper: prepare search request: %w", err)
	}
	request = prepared
	raw, err := c.search(ctx, buildSearchRequest(request))
	if err != nil {
		return nil, err
	}
	return raw.toSearchResponse(request.Query), nil
}

func buildSearchRequest(request *web.SearchRequest) *searchRequest {
	r := &searchRequest{
		Q:           request.QueryWithSiteOperators(),
		Autocorrect: true,
	}
	if request.MaxResults > 0 {
		r.Num = request.MaxResults
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
	results := make([]*web.SearchResult, 0, len(s.Organic))
	for _, searchResult := range s.Organic {
		if searchResult == nil {
			continue
		}
		results = append(results, &web.SearchResult{
			Title:         searchResult.Title,
			URL:           searchResult.Link,
			Snippet:       searchResult.Snippet,
			PublishedTime: parseDate(searchResult.Date),
		})
	}
	if s.SearchParameters.Q != "" {
		query = s.SearchParameters.Q
	}
	return &web.SearchResponse{Query: query, Results: results}
}

// parseDate tries Serper's common date formats. Relative strings
// ("2 days ago") are returned as zero time.
func parseDate(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{"Jan 2, 2006", time.DateOnly, time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}
