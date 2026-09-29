package perplexity

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
	baseURL              = "https://api.perplexity.ai"
	searchPath           = "/search"
	excludedDomainPrefix = "-"
)

// Config configures a [Client]. APIKey is required; an empty BaseURL selects
// Perplexity's public endpoint and a nil HTTPClient selects a default client.
type Config struct {
	APIKey     string
	BaseURL    string
	HTTPClient *http.Client
}

// Client implements [web.Searcher] against Perplexity's Search API.
type Client struct {
	http *resty.Client
}

var _ web.Searcher = (*Client)(nil)

func NewClient(config Config) (*Client, error) {
	if config.APIKey == "" {
		return nil, errors.New("perplexity: API key is required")
	}
	transport := providerhttp.NewClient(config.HTTPClient, cmp.Or(config.BaseURL, baseURL)).
		SetAuthToken(config.APIKey).
		SetHeader("Content-Type", "application/json")
	return &Client{http: transport}, nil
}

type searchRequest struct {
	Query               string   `json:"query"`
	MaxResults          int      `json:"max_results,omitzero"`
	SearchDomainFilter  []string `json:"search_domain_filter,omitempty"`
	SearchRecencyFilter string   `json:"search_recency_filter,omitempty"`
}

// newSearchRequest relies on web.SearchRequest.Prepare for the mutually
// exclusive domain lists and their 20-entry cap, which matches Perplexity's
// own limit on search_domain_filter.
func newSearchRequest(request *web.SearchRequest) *searchRequest {
	domains := request.AllowedDomains
	if len(request.BlockedDomains) > 0 {
		domains = make([]string, len(request.BlockedDomains))
		for index, domain := range request.BlockedDomains {
			domains[index] = excludedDomainPrefix + domain
		}
	}
	return &searchRequest{
		Query:               request.Query,
		MaxResults:          request.MaxResults,
		SearchDomainFilter:  domains,
		SearchRecencyFilter: string(request.Recency),
	}
}

type searchResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
	Date    string `json:"date,omitempty"`
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
			Title:         searchResult.Title,
			URL:           searchResult.URL,
			Snippet:       searchResult.Snippet,
			PublishedTime: parseDate(searchResult.Date),
		})
	}
	return &web.SearchResponse{Query: query, Results: results}
}

func (c *Client) Search(ctx context.Context, request *web.SearchRequest) (*web.SearchResponse, error) {
	prepared, err := request.Prepare()
	if err != nil {
		return nil, fmt.Errorf("perplexity: prepare search request: %w", err)
	}
	var raw searchResponse
	httpRequest := c.http.R().SetContext(ctx).SetBody(newSearchRequest(prepared))
	if _, err := providerhttp.Execute(httpRequest, http.MethodPost, searchPath, &raw); err != nil {
		return nil, fmt.Errorf("perplexity: search request: %w", err)
	}
	return raw.toSearchResponse(prepared.Query), nil
}

func parseDate(s string) time.Time {
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		return time.Time{}
	}
	return t
}
