package jina

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/go-resty/resty/v2"
	"github.com/samber/lo"

	"github.com/Tangerg/scope/tools/web"
	"github.com/Tangerg/scope/tools/web/internal/providerhttp"
)

const (
	searchBaseURL         = "https://s.jina.ai"
	fetchBaseURL          = "https://r.jina.ai"
	fetchPath             = "/"
	queryParameterCount   = "count"
	queryParameterPage    = "page"
	queryParameterSite    = "site"
	firstPage             = 1
	defaultSearchResults  = 10
	maximumSnippetRunes   = 300
	snippetEllipsis       = "..."
	respondWithHeader     = "X-Respond-With"
	respondWithoutContent = "no-content"
	retainImagesHeader    = "X-Retain-Images"
	retainNoImages        = "none"
)

// Config configures a [Client]. APIKey is required. Jina serves search and
// fetch from different endpoints, so each has its own base URL; an empty one
// selects Jina's public endpoint. A nil HTTPClient selects a default client.
type Config struct {
	APIKey        string
	SearchBaseURL string
	FetchBaseURL  string
	// Each endpoint owns its origin restriction; the host retains the shared client.
	HTTPClient *http.Client
}

type Client struct {
	searchHTTP *resty.Client
	fetchHTTP  *resty.Client
}

var _ web.Searcher = (*Client)(nil)

func NewClient(ctx context.Context, config Config) (*Client, error) {
	if config.APIKey == "" {
		return nil, errors.New("jina: API key is required")
	}
	searchHTTP, err := providerhttp.NewClient(ctx, providerhttp.Config{HTTPClient: config.HTTPClient, BaseURL: cmp.Or(config.SearchBaseURL, searchBaseURL)})
	if err != nil {
		return nil, fmt.Errorf("jina: search endpoint: %w", err)
	}
	fetchHTTP, err := providerhttp.NewClient(ctx, providerhttp.Config{HTTPClient: config.HTTPClient, BaseURL: cmp.Or(config.FetchBaseURL, fetchBaseURL)})
	if err != nil {
		return nil, fmt.Errorf("jina: fetch endpoint: %w", err)
	}
	return &Client{
		searchHTTP: searchHTTP.
			SetAuthToken(config.APIKey).
			SetHeader(respondWithHeader, respondWithoutContent),
		fetchHTTP: fetchHTTP.
			SetAuthToken(config.APIKey).
			SetHeader(retainImagesHeader, retainNoImages),
	}, nil
}

type searchRequest struct {
	Query string
	Count int
	Site  []string
}

func newSearchRequest(request *web.SearchRequest) *searchRequest {
	return &searchRequest{
		Query: request.Query,
		Count: cmp.Or(request.MaxResults, defaultSearchResults),
		Site:  request.AllowedDomains,
	}
}

func (s *searchRequest) path() string {
	return "/" + url.PathEscape(s.Query)
}

func (s *searchRequest) params() url.Values {
	parameters := url.Values{
		queryParameterCount: {strconv.Itoa(s.Count)},
		queryParameterPage:  {strconv.Itoa(firstPage)},
	}
	for _, site := range s.Site {
		parameters.Add(queryParameterSite, site)
	}
	return parameters
}

type searchResult struct {
	Title       string `json:"title"`
	URL         string `json:"url"`
	Description string `json:"description"`
	Content     string `json:"content,omitempty"`
	Date        string `json:"date,omitempty"`
}

func (s *searchResult) snippet() string {
	if s.Description != "" {
		return s.Description
	}
	if lo.RuneLength(s.Content) > maximumSnippetRunes {
		return lo.Substring(s.Content, 0, maximumSnippetRunes) + snippetEllipsis
	}
	return s.Content
}

type searchResponse struct {
	Data []*searchResult `json:"data"`
}

func (s *searchResponse) toSearchResponse(request *web.SearchRequest) (*web.SearchResponse, error) {
	if s.Data == nil {
		return nil, fmt.Errorf("jina: %w: response is missing data", web.ErrInvalidSearchResponse)
	}
	results := make([]*web.SearchResult, 0, len(s.Data))
	for index, searchResult := range s.Data {
		if searchResult == nil {
			return nil, fmt.Errorf("jina: %w: result %d is null", web.ErrInvalidSearchResponse, index)
		}
		results = append(results, &web.SearchResult{
			Title:         searchResult.Title,
			URL:           searchResult.URL,
			Snippet:       searchResult.snippet(),
			PublishedTime: parseDate(searchResult.Date),
		})
	}
	response := &web.SearchResponse{Query: request.Query, Results: results}
	if err := response.ValidateFor(request); err != nil {
		return nil, fmt.Errorf("jina: %w", err)
	}
	return response, nil
}

func (c *Client) Search(ctx context.Context, request *web.SearchRequest) (*web.SearchResponse, error) {
	prepared, err := request.Prepare()
	if err != nil {
		return nil, fmt.Errorf("jina: prepare search request: %w", err)
	}
	if len(prepared.BlockedDomains) > 0 {
		return nil, fmt.Errorf("jina: %w: blocked_domains", web.ErrUnsupportedFilter)
	}
	if prepared.Recency != "" {
		return nil, fmt.Errorf("jina: %w: recency", web.ErrUnsupportedFilter)
	}
	search := newSearchRequest(prepared)
	var raw searchResponse
	httpRequest := c.searchHTTP.R().SetContext(ctx).SetQueryParamsFromValues(search.params())
	if _, err := providerhttp.Execute(httpRequest, http.MethodGet, search.path(), &raw); err != nil {
		return nil, fmt.Errorf("jina: search request: %w", err)
	}
	return raw.toSearchResponse(prepared)
}

func (c *Client) Fetch(ctx context.Context, request *web.FetchRequest) (*web.FetchResponse, error) {
	prepared, err := request.Prepare()
	if err != nil {
		return nil, fmt.Errorf("jina: prepare fetch request: %w", err)
	}
	var raw fetchResponse
	httpRequest := c.fetchHTTP.R().SetContext(ctx).
		SetHeader(respondWithHeader, string(prepared.Format)).
		SetBody(&fetchRequest{URL: prepared.URL})
	if _, err := providerhttp.Execute(httpRequest, http.MethodPost, fetchPath, &raw); err != nil {
		return nil, fmt.Errorf("jina: fetch request: %w", err)
	}
	return raw.toFetchResponse(prepared)
}

func parseDate(s string) time.Time {
	for _, layout := range []string{"Jan 2, 2006", "02 Jan 2006", time.DateOnly, time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}
