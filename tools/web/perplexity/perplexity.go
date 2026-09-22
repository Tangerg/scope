package perplexity

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/go-resty/resty/v2"

	"github.com/Tangerg/scope/tools/web"
)

const (
	baseURL = "https://api.perplexity.ai"
)

// Config takes an optional [http.Client] so the host keeps ownership of
// timeouts, proxying, and transport instrumentation instead of inheriting
// whatever this package would otherwise choose.
type Config struct {
	APIKey     string
	BaseURL    string
	HTTPClient *http.Client
}

// Client implements [web.Searcher] against Perplexity's Search API and
// normalizes its link results into the shared web contract.
type Client struct {
	http *resty.Client
}

var _ web.Searcher = (*Client)(nil)

// NewClient requires the API key at construction so a missing credential fails
// at wiring rather than as an authorization error on a model's first search.
func NewClient(config Config) (*Client, error) {
	if config.APIKey == "" {
		return nil, errors.New("perplexity: API key is required")
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
	Query               string   `json:"query"`
	MaxResults          int      `json:"max_results,omitzero"`
	SearchDomainFilter  []string `json:"search_domain_filter,omitempty"`
	SearchRecencyFilter string   `json:"search_recency_filter,omitempty"`
}

func (s *searchRequest) validate() error {
	if s == nil {
		return errors.New("perplexity: search request must not be nil")
	}
	if s.Query == "" {
		return errors.New("perplexity: search query must not be empty")
	}
	return nil
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

func (c *Client) search(ctx context.Context, request *searchRequest) (*searchResponse, error) {
	if err := request.validate(); err != nil {
		return nil, err
	}
	var raw searchResponse
	response, err := c.http.R().SetContext(ctx).SetBody(request).SetResult(&raw).Post("/search")
	if err != nil {
		return nil, fmt.Errorf("perplexity: execute search request: %w", err)
	}
	if !response.IsSuccess() {
		return nil, fmt.Errorf("perplexity: search request returned HTTP %d: %s", response.StatusCode(), response.String())
	}
	return &raw, nil
}

func (c *Client) Search(ctx context.Context, request *web.SearchRequest) (*web.SearchResponse, error) {
	prepared, err := request.Prepare()
	if err != nil {
		return nil, fmt.Errorf("perplexity: prepare search request: %w", err)
	}
	request = prepared
	raw, err := c.search(ctx, buildSearchRequest(request))
	if err != nil {
		return nil, err
	}
	return raw.toSearchResponse(request.Query), nil
}

// maxDomainFilters is Perplexity's documented 20-entry cap on the
// search_domain_filter field.
const maxDomainFilters = 20

func buildSearchRequest(request *web.SearchRequest) *searchRequest {
	r := &searchRequest{Query: request.Query}
	if request.MaxResults > 0 {
		r.MaxResults = request.MaxResults
	}
	switch {
	case len(request.AllowedDomains) > 0:
		r.SearchDomainFilter = capDomains(request.AllowedDomains)
	case len(request.BlockedDomains) > 0:
		negated := capDomains(request.BlockedDomains)
		for i, d := range negated {
			negated[i] = "-" + d
		}
		r.SearchDomainFilter = negated
	}
	r.SearchRecencyFilter = recencyToString(request.Recency)
	return r
}

// capDomains caps the slice at Perplexity's documented 20-entry
// limit and returns a copy so the caller's slice isn't mutated
// downstream (the negation loop in buildSearchRequest writes in place).
func capDomains(in []string) []string {
	return slices.Clone(in[:min(len(in), maxDomainFilters)])
}

func recencyToString(r web.Recency) string {
	switch r {
	case web.RecencyHour, web.RecencyDay, web.RecencyWeek, web.RecencyMonth, web.RecencyYear:
		return string(r)
	}
	return ""
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
	// Perplexity doesn't echo the query; pass through the requester's.
	return &web.SearchResponse{Query: query, Results: results}
}

func parseDate(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		return time.Time{}
	}
	return t
}
