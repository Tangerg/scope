package serper

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/Tangerg/scope/tools/web/internal/googletbs"

	"github.com/go-resty/resty/v2"

	"github.com/Tangerg/scope/tools/web"
	"github.com/Tangerg/scope/tools/web/internal/providerhttp"
)

const (
	baseURL      = "https://google.serper.dev"
	searchPath   = "/search"
	apiKeyHeader = "X-API-KEY"
)

// Config configures a [Client]. APIKey is required; an empty BaseURL selects
// Serper's public endpoint and a nil HTTPClient selects a default client.
type Config struct {
	APIKey     string
	BaseURL    string
	HTTPClient *http.Client
}

type Client struct {
	http *resty.Client
}

var _ web.Searcher = (*Client)(nil)

func NewClient(config Config) (*Client, error) {
	if config.APIKey == "" {
		return nil, errors.New("serper: API key is required")
	}
	transport := providerhttp.NewClient(config.HTTPClient, cmp.Or(config.BaseURL, baseURL)).
		SetHeader(apiKeyHeader, config.APIKey).
		SetHeader("Content-Type", "application/json")
	return &Client{http: transport}, nil
}

type searchRequest struct {
	Q           string `json:"q"`
	Num         int    `json:"num,omitzero"`
	Autocorrect bool   `json:"autocorrect,omitzero"`
	Tbs         string `json:"tbs,omitempty"`
}

func newSearchRequest(request *web.SearchRequest) *searchRequest {
	return &searchRequest{
		Q:           request.QueryWithSiteOperators(),
		Num:         request.MaxResults,
		Autocorrect: true,
		Tbs:         googletbs.FromRecency(request.Recency),
	}
}

type organicResult struct {
	Title   string `json:"title"`
	Link    string `json:"link"`
	Snippet string `json:"snippet"`
	Date    string `json:"date,omitempty"`
}

type searchResponse struct {
	Organic []*organicResult `json:"organic"`
}

func (s *searchResponse) toSearchResponse(query string) (*web.SearchResponse, error) {
	results := make([]*web.SearchResult, 0, len(s.Organic))
	for index, searchResult := range s.Organic {
		if searchResult == nil {
			return nil, fmt.Errorf("serper: %w: result %d is null", web.ErrInvalidSearchResponse, index)
		}
		results = append(results, &web.SearchResult{
			Title:         searchResult.Title,
			URL:           searchResult.Link,
			Snippet:       searchResult.Snippet,
			PublishedTime: parseDate(searchResult.Date),
		})
	}
	response := &web.SearchResponse{Query: query, Results: results}
	if err := response.Validate(); err != nil {
		return nil, fmt.Errorf("serper: %w", err)
	}
	return response, nil
}

func (c *Client) Search(ctx context.Context, request *web.SearchRequest) (*web.SearchResponse, error) {
	prepared, err := request.Prepare()
	if err != nil {
		return nil, fmt.Errorf("serper: prepare search request: %w", err)
	}
	var raw searchResponse
	httpRequest := c.http.R().SetContext(ctx).SetBody(newSearchRequest(prepared))
	if _, err := providerhttp.Execute(httpRequest, http.MethodPost, searchPath, &raw); err != nil {
		return nil, fmt.Errorf("serper: search request: %w", err)
	}
	return raw.toSearchResponse(prepared.Query)
}

func parseDate(s string) time.Time {
	for _, layout := range []string{"Jan 2, 2006", time.DateOnly, time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}
