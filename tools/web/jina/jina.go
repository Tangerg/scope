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
)

const (
	searchBaseURL         = "https://s.jina.ai"
	fetchBaseURL          = "https://r.jina.ai"
	queryParameterCount   = "count"
	queryParameterPage    = "page"
	queryParameterSite    = "site"
	defaultSearchResults  = 10
	maximumSnippetRunes   = 300
	snippetEllipsis       = "..."
	mediaTypeJSON         = "application/json"
	respondWithHeader     = "X-Respond-With"
	respondWithoutContent = "no-content"
)

// Config carries separate search and fetch base URLs because Jina serves the
// two capabilities from different endpoints; one shared base would silently
// send half the calls to the wrong service.
type Config struct {
	APIKey        string
	SearchBaseURL string
	FetchBaseURL  string
	HTTPClient    *http.Client
}

// Client implements the web contracts against Jina's reader and search
// endpoints. It holds two HTTP clients for the two base URLs rather than
// rewriting the host per call, so each endpoint's configuration stays
// independent.
type Client struct {
	searchHTTP *resty.Client
	fetchHTTP  *resty.Client
}

var _ web.Searcher = (*Client)(nil)

// NewClient requires the API key at construction so a missing credential fails
// at wiring rather than as an authorization error on a model's first call.
func NewClient(config Config) (*Client, error) {
	if config.APIKey == "" {
		return nil, errors.New("jina: API key is required")
	}
	if config.SearchBaseURL == "" {
		config.SearchBaseURL = searchBaseURL
	}
	if config.FetchBaseURL == "" {
		config.FetchBaseURL = fetchBaseURL
	}
	if config.HTTPClient == nil {
		config.HTTPClient = &http.Client{}
	}
	return &Client{
		searchHTTP: resty.NewWithClient(config.HTTPClient).
			SetBaseURL(config.SearchBaseURL).
			SetAuthToken(config.APIKey).
			SetHeader("Accept", mediaTypeJSON).
			SetHeader(respondWithHeader, respondWithoutContent),
		fetchHTTP: resty.NewWithClient(config.HTTPClient).
			SetBaseURL(config.FetchBaseURL).
			SetAuthToken(config.APIKey).
			SetHeader("Content-Type", mediaTypeJSON).
			SetHeader("Accept", mediaTypeJSON),
	}, nil
}

type searchRequest struct {
	Query string   `json:"-"`
	Count int      `json:"count,omitempty"`
	Page  int      `json:"page,omitempty"`
	Site  []string `json:"site,omitempty"`
}

func (s *searchRequest) validate() error {
	if s == nil {
		return errors.New("jina: search request must not be nil")
	}
	if s.Query == "" {
		return errors.New("jina: search query must not be empty")
	}
	return nil
}

type searchResult struct {
	Title       string `json:"title"`
	URL         string `json:"url"`
	Description string `json:"description"`
	Content     string `json:"content,omitempty"`
	Date        string `json:"date,omitempty"`
}

type searchResponse struct {
	Data []*searchResult `json:"data"`
}

func (c *Client) search(ctx context.Context, request *searchRequest) (*searchResponse, error) {
	if err := request.validate(); err != nil {
		return nil, err
	}
	endpoint := "/" + url.PathEscape(request.Query)
	params := request.params()
	httpRequest := c.searchHTTP.R().SetContext(ctx).SetQueryParamsFromValues(params)
	var raw searchResponse
	response, err := httpRequest.SetResult(&raw).Get(endpoint)
	if err != nil {
		return nil, fmt.Errorf("jina: execute search request: %w", err)
	}
	if !response.IsSuccess() {
		return nil, fmt.Errorf("jina: search request returned HTTP %d: %s", response.StatusCode(), response.String())
	}
	return &raw, nil
}

func (s *searchRequest) params() url.Values {
	parameters := make(url.Values)
	if s.Count > 0 {
		parameters.Set(queryParameterCount, strconv.Itoa(s.Count))
	}
	if s.Page > 0 {
		parameters.Set(queryParameterPage, strconv.Itoa(s.Page))
	}
	for _, site := range s.Site {
		parameters.Add(queryParameterSite, site)
	}
	return parameters
}

func (c *Client) Search(ctx context.Context, request *web.SearchRequest) (*web.SearchResponse, error) {
	prepared, err := request.Prepare()
	if err != nil {
		return nil, fmt.Errorf("jina: prepare search request: %w", err)
	}
	request = prepared
	if len(request.BlockedDomains) > 0 {
		return nil, fmt.Errorf("jina: %w: blocked_domains", web.ErrUnsupportedFilter)
	}
	if request.Recency != "" {
		return nil, fmt.Errorf("jina: %w: recency", web.ErrUnsupportedFilter)
	}
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
	httpRequest := c.fetchHTTP.R().SetContext(ctx).
		SetBody(request).
		SetHeader("X-Retain-Images", "none")
	if request.ReturnFormat != "" {
		httpRequest.SetHeader(respondWithHeader, request.ReturnFormat)
	}

	var raw fetchResponse
	response, err := httpRequest.SetResult(&raw).Post("/")
	if err != nil {
		return nil, fmt.Errorf("jina: execute fetch request: %w", err)
	}
	if !response.IsSuccess() {
		return nil, fmt.Errorf("jina: fetch request returned HTTP %d: %s", response.StatusCode(), response.String())
	}
	return &raw, nil
}

func (c *Client) Fetch(ctx context.Context, request *web.FetchRequest) (*web.FetchResponse, error) {
	prepared, err := request.Prepare()
	if err != nil {
		return nil, fmt.Errorf("jina: prepare fetch request: %w", err)
	}
	request = prepared
	format := request.Format
	raw, err := c.fetch(ctx, &fetchRequest{URL: request.URL, ReturnFormat: string(format)})
	if err != nil {
		return nil, err
	}
	return &web.FetchResponse{Content: raw.Data.Content, Format: format}, nil
}

func buildSearchRequest(request *web.SearchRequest) *searchRequest {
	r := &searchRequest{
		Query: request.Query,
		Count: cmp.Or(request.MaxResults, defaultSearchResults),
		Page:  1,
	}
	if len(request.AllowedDomains) > 0 {
		r.Site = request.AllowedDomains
	}
	return r
}

func (s *searchResponse) toSearchResponse(query string) *web.SearchResponse {
	results := make([]*web.SearchResult, 0, len(s.Data))
	for _, searchResult := range s.Data {
		if searchResult == nil {
			continue
		}
		results = append(results, &web.SearchResult{
			Title:         searchResult.Title,
			URL:           searchResult.URL,
			Snippet:       searchResult.snippet(),
			PublishedTime: parseDate(searchResult.Date),
		})
	}
	return &web.SearchResponse{Query: query, Results: results}
}

func (s *searchResult) snippet() string {
	if s.Description != "" {
		return s.Description
	}
	if s.Content == "" {
		return ""
	}
	if lo.RuneLength(s.Content) > maximumSnippetRunes {
		return lo.Substring(s.Content, 0, maximumSnippetRunes) + snippetEllipsis
	}
	return s.Content
}

func parseDate(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{"Jan 2, 2006", "02 Jan 2006", time.DateOnly, time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}
