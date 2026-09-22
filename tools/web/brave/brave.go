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
)

const defaultSearchResultCount = 10

const (
	baseURL                 = "https://api.search.brave.com/res/v1"
	queryParameterQuery     = "q"
	queryParameterCount     = "count"
	queryParameterFreshness = "freshness"
)

// Config takes an optional [http.Client] so the host keeps ownership of
// timeouts, proxying, and transport instrumentation instead of inheriting
// whatever this package would otherwise choose.
type Config struct {
	APIKey     string
	BaseURL    string
	HTTPClient *http.Client
}

// Client implements [web.Searcher] against the Brave API. It exposes no
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
		return nil, errors.New("brave: API key is required")
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
			SetHeader("X-Subscription-Token", config.APIKey).
			SetHeader("Accept", "application/json"),
	}, nil
}

type searchRequest struct {
	Q         string `json:"-"`
	Count     int    `json:"-"`
	Freshness string `json:"-"`
}

func (s *searchRequest) validate() error {
	if s == nil {
		return errors.New("brave: search request must not be nil")
	}
	if s.Q == "" {
		return errors.New("brave: search query must not be empty")
	}
	return nil
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

type queryInfo struct {
	Original string `json:"original"`
}

type searchResponse struct {
	Query queryInfo   `json:"query"`
	Web   *webResults `json:"web,omitzero"`
}

func (c *Client) search(ctx context.Context, request *searchRequest) (*searchResponse, error) {
	if err := request.validate(); err != nil {
		return nil, err
	}
	var raw searchResponse
	response, err := c.http.R().SetContext(ctx).SetQueryParams(request.params()).SetResult(&raw).Get("/web/search")
	if err != nil {
		return nil, fmt.Errorf("brave: execute search request: %w", err)
	}
	if !response.IsSuccess() {
		return nil, fmt.Errorf("brave: search request returned HTTP %d: %s", response.StatusCode(), response.String())
	}
	return &raw, nil
}

func (s *searchRequest) params() map[string]string {
	parameters := map[string]string{queryParameterQuery: s.Q}
	if s.Count > 0 {
		parameters[queryParameterCount] = strconv.Itoa(s.Count)
	}
	if s.Freshness != "" {
		parameters[queryParameterFreshness] = s.Freshness
	}
	return parameters
}

func (c *Client) Search(ctx context.Context, request *web.SearchRequest) (*web.SearchResponse, error) {
	prepared, err := request.Prepare()
	if err != nil {
		return nil, fmt.Errorf("brave: prepare search request: %w", err)
	}
	request = prepared
	if request.Recency == web.RecencyHour {
		return nil, fmt.Errorf("brave: %w: hourly recency", web.ErrUnsupportedFilter)
	}
	raw, err := c.search(ctx, buildSearchRequest(request))
	if err != nil {
		return nil, err
	}
	return raw.toSearchResponse(request.Query), nil
}

func buildSearchRequest(request *web.SearchRequest) *searchRequest {
	r := &searchRequest{
		Q:     request.QueryWithSiteOperators(),
		Count: cmp.Or(request.MaxResults, defaultSearchResultCount),
	}
	r.Freshness = recencyToFreshness(request.Recency)
	return r
}

func recencyToFreshness(r web.Recency) string {
	switch r {
	case web.RecencyDay:
		return "pd"
	case web.RecencyWeek:
		return "pw"
	case web.RecencyMonth:
		return "pm"
	case web.RecencyYear:
		return "py"
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
	return &web.SearchResponse{Query: cmp.Or(s.Query.Original, query), Results: results}
}

func parseAge(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t
	}
	return time.Time{}
}
