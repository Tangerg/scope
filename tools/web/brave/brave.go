package brave

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/go-resty/resty/v2"

	"github.com/Tangerg/scope/tools/internal/toolresult"
	"github.com/Tangerg/scope/tools/web"
	"github.com/Tangerg/scope/tools/web/internal/providerhttp"
)

const (
	baseURL                  = "https://api.search.brave.com/res/v1"
	searchPath               = "/web/search"
	defaultSearchResultCount = 10
	maximumQueryCharacters   = 600
	maximumQueryWords        = 75
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
		return nil, errors.New("brave: API key is required")
	}
	transport, err := providerhttp.NewClient(ctx, providerhttp.Config{HTTPClient: config.HTTPClient, BaseURL: cmp.Or(config.BaseURL, baseURL)})
	if err != nil {
		return nil, fmt.Errorf("brave: %w", err)
	}
	transport.SetHeader(subscriptionTokenHeader, config.APIKey)
	return &Client{http: transport}, nil
}

type searchRequest struct {
	Q         string
	Count     int
	Freshness string
}

func newSearchRequest(request *web.SearchRequest) (*searchRequest, error) {
	query := request.QueryWithSiteOperators()
	if utf8.RuneCountInString(query) > maximumQueryCharacters || len(strings.Fields(query)) > maximumQueryWords {
		return nil, fmt.Errorf("brave: %w: final query must contain at most %d characters and %d words", web.ErrInvalidQuery, maximumQueryCharacters, maximumQueryWords)
	}
	freshness, supported := recencyFreshness(request.Recency)
	if !supported {
		return nil, fmt.Errorf("brave: %w: %s recency", web.ErrUnsupportedFilter, request.Recency)
	}
	return &searchRequest{
		Q:         query,
		Count:     cmp.Or(request.MaxResults, defaultSearchResultCount),
		Freshness: freshness,
	}, nil
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
		cause := fmt.Errorf("brave: prepare search request: %w", err)
		return nil, toolresult.Correctable(cause.Error(), cause)
	}
	native, err := newSearchRequest(prepared)
	if err != nil {
		return nil, toolresult.Correctable(err.Error(), err)
	}
	var raw searchResponse
	httpRequest := c.http.R().SetContext(ctx).SetQueryParams(native.params())
	if err := providerhttp.Execute(httpRequest, http.MethodGet, searchPath, &raw); err != nil {
		return nil, fmt.Errorf("brave: search request: %w", err)
	}
	return raw.toSearchResponse(prepared)
}

// recencyFreshness maps a recency onto Brave's freshness window. Brave has no
// hourly window, and an unset recency sends none.
func recencyFreshness(recency web.Recency) (string, bool) {
	switch recency {
	case "":
		return "", true
	case web.RecencyDay:
		return freshnessPastDay, true
	case web.RecencyWeek:
		return freshnessPastWeek, true
	case web.RecencyMonth:
		return freshnessPastMonth, true
	case web.RecencyYear:
		return freshnessPastYear, true
	default:
		return "", false
	}
}

func (s *searchResponse) toSearchResponse(request *web.SearchRequest) (*web.SearchResponse, error) {
	var results []*web.SearchResult
	if s.Web != nil {
		results = make([]*web.SearchResult, 0, len(s.Web.Results))
		for index, searchResult := range s.Web.Results {
			if searchResult == nil {
				return nil, fmt.Errorf("brave: %w: result %d is null", web.ErrInvalidSearchResponse, index)
			}
			results = append(results, &web.SearchResult{
				Title:   searchResult.Title,
				URL:     searchResult.URL,
				Snippet: searchResult.Description,
			})
		}
	}
	response := &web.SearchResponse{Query: request.Query, Results: results}
	if err := response.ValidateFor(request); err != nil {
		return nil, fmt.Errorf("brave: %w", err)
	}
	return response, nil
}
