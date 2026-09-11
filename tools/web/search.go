package web

import (
	"context"
	"fmt"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/idna"
)

// Recency is a coarse "last N period" filter. Providers map this to
// their native syntax (e.g. Tavily's time_range, Serper's tbs=qdr:).
type Recency string

// Recency windows are a closed vocabulary so the same freshness request means
// the same thing across backends that each spell it differently.
const (
	RecencyHour  Recency = "hour"
	RecencyDay   Recency = "day"
	RecencyWeek  Recency = "week"
	RecencyMonth Recency = "month"
	RecencyYear  Recency = "year"
)

func (r Recency) Validate() error {
	switch r {
	case "", RecencyHour, RecencyDay, RecencyWeek, RecencyMonth, RecencyYear:
		return nil
	default:
		return ErrInvalidRecency
	}
}

// SearchRequest is both the provider-neutral search contract and the
// LLM-facing argument shape.
type SearchRequest struct {
	// Query is the search string. Required.
	Query string `json:"query" jsonschema:"minLength=1" jsonschema_description:"Non-empty web search query. Include the current year when asking for the latest information."`

	// MaxResults caps the number of returned results. 0 = use the
	// provider's default (typically 5-10).
	MaxResults int `json:"max_results,omitempty" jsonschema:"minimum=1,maximum=20" jsonschema_description:"Maximum results to return, from 1 to 20. Omit to use the configured search default (typically 5-10)."`

	// AllowedDomains restricts results to any of these domains. Mutually
	// exclusive with BlockedDomains on most providers.
	AllowedDomains []string `json:"allowed_domains,omitempty" jsonschema:"maxItems=20" jsonschema_description:"Only include results from at most 20 domains (bare domain names, no protocol). Mutually exclusive with blocked_domains."`

	// BlockedDomains drops results from these domains.
	BlockedDomains []string `json:"blocked_domains,omitempty" jsonschema:"maxItems=20" jsonschema_description:"Exclude results from at most 20 domains (bare domain names, no protocol). Mutually exclusive with allowed_domains."`

	// Recency filters to a coarse time-window. "" = no time filter.
	Recency Recency `json:"recency,omitempty" jsonschema:"enum=hour,enum=day,enum=week,enum=month,enum=year" jsonschema_description:"Optional time window: hour, day, week, month, or year."`
}

// Prepare returns an independently owned, normalized request after validating
// it. The receiver is never mutated.
func (s *SearchRequest) Prepare() (*SearchRequest, error) {
	if s == nil {
		return nil, ErrMissingSearchRequest
	}
	prepared := *s
	prepared.Query = strings.TrimSpace(s.Query)
	if err := prepared.Validate(); err != nil {
		return nil, err
	}
	prepared.AllowedDomains, _ = normalizeDomains(s.AllowedDomains)
	prepared.BlockedDomains, _ = normalizeDomains(s.BlockedDomains)
	return &prepared, nil
}

func (s *SearchRequest) Validate() error {
	if s == nil {
		return ErrMissingSearchRequest
	}
	if strings.TrimSpace(s.Query) == "" {
		return ErrEmptyQuery
	}
	if s.MaxResults < 0 || s.MaxResults > 20 {
		return ErrInvalidMaxResults
	}
	if len(s.AllowedDomains) > 0 && len(s.BlockedDomains) > 0 {
		return ErrDomainsBothSides
	}
	if len(s.AllowedDomains) > 20 || len(s.BlockedDomains) > 20 {
		return ErrTooManyDomains
	}
	if _, err := normalizeDomains(s.AllowedDomains); err != nil {
		return err
	}
	if _, err := normalizeDomains(s.BlockedDomains); err != nil {
		return err
	}
	return s.Recency.Validate()
}

func normalizeDomains(domains []string) ([]string, error) {
	normalized := make([]string, 0, len(domains))
	seen := make(map[string]struct{}, len(domains))
	for _, raw := range domains {
		domain, err := normalizeDomain(raw)
		if err != nil {
			return nil, err
		}
		if _, exists := seen[domain]; exists {
			continue
		}
		seen[domain] = struct{}{}
		normalized = append(normalized, domain)
	}
	return normalized, nil
}

func normalizeDomain(raw string) (string, error) {
	domain := strings.TrimSuffix(strings.TrimSpace(raw), ".")
	if domain == "" || strings.ContainsAny(domain, "/?#@*") || strings.Contains(domain, ":") {
		return "", fmt.Errorf("%w: %q", ErrInvalidDomain, raw)
	}
	if address, err := netip.ParseAddr(domain); err == nil {
		return address.String(), nil
	}
	ascii, err := idna.Lookup.ToASCII(domain)
	if err != nil {
		return "", fmt.Errorf("%w: %q: %w", ErrInvalidDomain, raw, err)
	}
	ascii = strings.ToLower(strings.TrimSuffix(ascii, "."))
	if len(ascii) > 253 {
		return "", fmt.Errorf("%w: %q", ErrInvalidDomain, raw)
	}
	for _, label := range strings.Split(ascii, ".") {
		if !validDomainLabel(label) {
			return "", fmt.Errorf("%w: %q", ErrInvalidDomain, raw)
		}
	}
	return ascii, nil
}

func validDomainLabel(label string) bool {
	if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
		return false
	}
	for _, character := range label {
		if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '-' {
			return false
		}
	}
	return true
}

// QueryWithSiteOperators returns Query with Google-style site:/-site:
// operators for the request's domain filters. Providers without native domain
// fields use this projection. Allowed domains form an OR group; every blocked
// domain is excluded. Empty domain entries are ignored.
func (s *SearchRequest) QueryWithSiteOperators() string {
	if s == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString(s.Query)
	var allowed []string
	for _, domain := range s.AllowedDomains {
		if domain != "" {
			allowed = append(allowed, "site:"+domain)
		}
	}
	if len(allowed) == 1 {
		fmt.Fprintf(&b, " %s", allowed[0])
	} else if len(allowed) > 1 {
		fmt.Fprintf(&b, " (%s)", strings.Join(allowed, " OR "))
	}
	for _, domain := range s.BlockedDomains {
		if domain != "" {
			fmt.Fprintf(&b, " -site:%s", domain)
		}
	}
	return b.String()
}

// SearchResult is one normalized search hit.
type SearchResult struct {
	Title         string    `json:"title"`
	URL           string    `json:"url"`
	Snippet       string    `json:"snippet"`
	FaviconURL    string    `json:"favicon_url,omitempty"`
	PublishedTime time.Time `json:"published_time,omitzero"`
	Source        string    `json:"source,omitempty"`
}

// SearchResponse carries the executed query plus normalized results. Used
// as both the SPI return type and the LLM-facing serialization shape.
type SearchResponse struct {
	Query   string          `json:"query"`
	Results []*SearchResult `json:"results"`
}

func (s *SearchResponse) Validate() error {
	if s == nil {
		return ErrMissingSearchResponse
	}
	if strings.TrimSpace(s.Query) == "" {
		return fmt.Errorf("%w: query is blank", ErrInvalidSearchResponse)
	}
	for index, result := range s.Results {
		if result == nil {
			return fmt.Errorf("%w: result %d is nil", ErrInvalidSearchResponse, index)
		}
		parsed, err := url.Parse(strings.TrimSpace(result.URL))
		if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return fmt.Errorf("%w: result %d has invalid URL %q", ErrInvalidSearchResponse, index, result.URL)
		}
	}
	return nil
}

// Searcher is the provider boundary behind the model-facing search tool. It
// receives only the normalized provider-neutral contract; authentication,
// endpoint selection, and provider defaults are frozen in the implementation.
// Implementations must support concurrent calls, including calls through
// other tools sharing the same backend; search tools advertise parallel use.
type Searcher interface {
	// Search performs one request without mutating or retaining it and transfers
	// ownership of a normalized response to the caller. Implementations must
	// honor ctx, preserve provider error causes, and reject unsupported explicit
	// fields instead of silently ignoring them.
	Search(ctx context.Context, request *SearchRequest) (*SearchResponse, error)
}
