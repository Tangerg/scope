package brave

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/go-resty/resty/v2"

	"github.com/Tangerg/scope/tools/web"
	"github.com/Tangerg/scope/tools/web/internal/providerhttp"
)

const (
	baseURL                  = "https://api.search.brave.com/res/v1"
	searchPath               = "/web/search"
	defaultSearchResultCount = 10
	queryParameterQuery      = "q"
	queryParameterCount      = "count"
	queryParameterFreshness  = "freshness"
	subscriptionTokenHeader  = "X-Subscription-Token"
	freshnessPastDay         = "pd"
	freshnessPastWeek        = "pw"
	freshnessPastMonth       = "pm"
	freshnessPastYear        = "py"
)

// Config configures a [Client]. APIKey is required; an empty BaseURL selects
// Brave's public endpoint and a nil HTTPClient selects a default client.
type Config struct {
	APIKey     string
	BaseURL    string
	HTTPClient *http.Client
}

// Client implements [web.Searcher] against the Brave Web Search API.
type Client struct {
	http *resty.Client
}

var _ web.Searcher = (*Client)(nil)

func NewClient(config Config) (*Client, error) {
	if config.APIKey == "" {
		return nil, errors.New("brave: API key is required")
	}
	transport := providerhttp.NewClient(config.HTTPClient, cmp.Or(config.BaseURL, baseURL)).
		SetHeader(subscriptionTokenHeader, config.APIKey).
		SetHeader("Accept", "application/json")
	return &Client{http: transport}, nil
}

type searchRequest struct {
	Q         string
	Count     int
	Freshness string
}

func newSearchRequest(request *web.SearchRequest) *searchRequest {
	return &searchRequest{
		Q:         request.QueryWithSiteOperators(),
		Count:     cmp.Or(request.MaxResults, defaultSearchResultCount),
		Freshness: recencyToFreshness(request.Recency),
	}
}

func (s *searchRequest) params() map[string]string {
	parameters := map[string]string{
		queryParameterQuery: s.Q,
		queryParameterCount: strconv.Itoa(s.Count),
	}
	if s.Freshness != "" {
		parameters[queryParameterFreshness] = s.Freshness
	}
	return parameters
}

type searchResult struct {
	Title       string `json:"title"`
	URL         string `json:"url"`
	Description string `json:"description"`
	PageAge     string `json:"page_age,omitempty"`
}

type webResults struct {
	Results []*searchResult `json:"results"`
}

type searchResponse struct {
	Web *webResults `json:"web,omitzero"`
}

func (c *Client) Search(ctx context.Context, request *web.SearchRequest) (*web.SearchResponse, error) {
	prepared, err := request.Prepare()
	if err != nil {
		return nil, fmt.Errorf("brave: prepare search request: %w", err)
	}
	if prepared.Recency == web.RecencyHour {
		return nil, fmt.Errorf("brave: %w: hourly recency", web.ErrUnsupportedFilter)
	}
	var raw searchResponse
	httpRequest := c.http.R().SetContext(ctx).SetQueryParams(newSearchRequest(prepared).params())
	if _, err := providerhttp.Execute(httpRequest, http.MethodGet, searchPath, &raw); err != nil {
		return nil, fmt.Errorf("brave: search request: %w", err)
	}
	return raw.toSearchResponse(prepared.Query), nil
}

func recencyToFreshness(r web.Recency) string {
	switch r {
	case web.RecencyDay:
		return freshnessPastDay
	case web.RecencyWeek:
		return freshnessPastWeek
	case web.RecencyMonth:
		return freshnessPastMonth
	case web.RecencyYear:
		return freshnessPastYear
	}
	return ""
}

func (s *searchResponse) toSearchResponse(query string) *web.SearchResponse {
	var results []*web.SearchResult
	if s.Web != nil {
		results = make([]*web.SearchResult, 0, len(s.Web.Results))
		for _, searchResult := range s.Web.Results {
			if searchResult == nil {
				continue
			}
			results = append(results, &web.SearchResult{
				Title:         searchResult.Title,
				URL:           searchResult.URL,
				Snippet:       searchResult.Description,
				PublishedTime: parseAge(searchResult.PageAge),
			})
		}
	}
	return &web.SearchResponse{Query: query, Results: results}
}

func parseAge(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}
