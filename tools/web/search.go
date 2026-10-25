package web

import (
	"context"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/net/idna"

	"github.com/Tangerg/scope/core/jsonschema"
	"github.com/Tangerg/scope/core/timestamp"
)

const (
	maxSearchResults = 20
	maxDomainFilters = 20
	// DNS limits from RFC 1035, applied to the ASCII form of a domain.
	maxDomainLength           = 253
	maxDomainLabelLength      = 63
	siteQueryOperator         = "site:"
	negativeSiteQueryOperator = "-site:"
	queryDisjunction          = " OR "
)

// Recency expresses a relative freshness window shared across backends.
type Recency string

const (
	RecencyHour  Recency = "hour"
	RecencyDay   Recency = "day"
	RecencyWeek  Recency = "week"
	RecencyMonth Recency = "month"
	RecencyYear  Recency = "year"
)

var recencyVocabulary = [...]Recency{RecencyHour, RecencyDay, RecencyWeek, RecencyMonth, RecencyYear}

func (Recency) JSONSchemaEnum() []string {
	values := make([]string, len(recencyVocabulary))
	for index, value := range recencyVocabulary {
		values[index] = string(value)
	}
	return values
}

func (r Recency) Validate() error {
	if r == "" || slices.Contains(recencyVocabulary[:], r) {
		return nil
	}
	return ErrInvalidRecency
}

type SearchRequest struct {
	Query string `json:"query" jsonschema:"minLength=1" jsonschema_description:"Non-empty web search query. Include the current year when asking for the latest information."`

	MaxResults int `json:"max_results,omitzero" jsonschema_description:"Maximum results to return within the supported search limit. Omit to use the configured search default (typically 5-10)."`

	AllowedDomains []string `json:"allowed_domains,omitempty" jsonschema_description:"Only include results from the allowed domains (bare domain names, no protocol). Mutually exclusive with blocked_domains."`

	BlockedDomains []string `json:"blocked_domains,omitempty" jsonschema_description:"Exclude results from these domains (bare domain names, no protocol). Mutually exclusive with allowed_domains."`

	Recency Recency `json:"recency,omitempty" jsonschema_description:"Optional time window: hour, day, week, month, or year."`
}

// Prepare returns an independently owned, normalized request after validating
// it. The receiver is never mutated. It normalizes each domain list once, keeping
// the result Validate would otherwise compute and discard.
func (s *SearchRequest) Prepare() (*SearchRequest, error) {
	if s == nil {
		return nil, ErrMissingSearchRequest
	}
	prepared := *s
	prepared.Query = strings.TrimSpace(s.Query)
	if err := prepared.validateQuery(); err != nil {
		return nil, err
	}
	allowed, blocked, err := prepared.normalizeFilters()
	if err != nil {
		return nil, err
	}
	prepared.AllowedDomains, prepared.BlockedDomains = allowed, blocked
	if err := prepared.Recency.Validate(); err != nil {
		return nil, err
	}
	return &prepared, nil
}

func (s *SearchRequest) Validate() error {
	if s == nil {
		return ErrMissingSearchRequest
	}
	if err := s.validateQuery(); err != nil {
		return err
	}
	if _, _, err := s.normalizeFilters(); err != nil {
		return err
	}
	return s.Recency.Validate()
}

func (s *SearchRequest) validateQuery() error {
	if !utf8.ValidString(s.Query) {
		return ErrInvalidQuery
	}
	if strings.TrimSpace(s.Query) == "" {
		return ErrEmptyQuery
	}
	if s.MaxResults < 0 || s.MaxResults > maxSearchResults {
		return ErrInvalidMaxResults
	}
	return nil
}

func (s *SearchRequest) normalizeFilters() (allowed, blocked []string, err error) {
	if len(s.AllowedDomains) > 0 && len(s.BlockedDomains) > 0 {
		return nil, nil, ErrDomainsBothSides
	}
	if len(s.AllowedDomains) > maxDomainFilters || len(s.BlockedDomains) > maxDomainFilters {
		return nil, nil, ErrTooManyDomains
	}
	allowed, err = normalizeDomains(s.AllowedDomains)
	if err != nil {
		return nil, nil, err
	}
	blocked, err = normalizeDomains(s.BlockedDomains)
	if err != nil {
		return nil, nil, err
	}
	return allowed, blocked, nil
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
	if len(ascii) > maxDomainLength {
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
	if label == "" || len(label) > maxDomainLabelLength || label[0] == '-' || label[len(label)-1] == '-' {
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
			allowed = append(allowed, siteQueryOperator+domain)
		}
	}
	if len(allowed) == 1 {
		fmt.Fprintf(&b, " %s", allowed[0])
	} else if len(allowed) > 1 {
		fmt.Fprintf(&b, " (%s)", strings.Join(allowed, queryDisjunction))
	}
	for _, domain := range s.BlockedDomains {
		if domain != "" {
			fmt.Fprintf(&b, " %s%s", negativeSiteQueryOperator, domain)
		}
	}
	return b.String()
}

type SearchResult struct {
	Title         string    `json:"title"`
	URL           string    `json:"url"`
	Snippet       string    `json:"snippet"`
	FaviconURL    string    `json:"favicon_url,omitempty"`
	PublishedTime time.Time `json:"published_time,omitzero"`
	Source        string    `json:"source,omitempty"`
}

func (s *SearchResult) Validate() error {
	if s == nil {
		return fmt.Errorf("%w: result is nil", ErrInvalidSearchResponse)
	}
	if !isHTTPURL(s.URL) {
		return fmt.Errorf("%w: result has invalid URL %q", ErrInvalidSearchResponse, s.URL)
	}
	for _, field := range []struct{ name, value string }{
		{"title", s.Title},
		{"snippet", s.Snippet},
		{"favicon_url", s.FaviconURL},
		{"source", s.Source},
	} {
		if !utf8.ValidString(field.value) {
			return fmt.Errorf("%w: result %s must be valid UTF-8", ErrInvalidSearchResponse, field.name)
		}
	}
	if err := timestamp.Validate(s.PublishedTime); err != nil {
		return fmt.Errorf("%w: published_time: %w", ErrInvalidSearchResponse, err)
	}
	return nil
}

type SearchResponse struct {
	// Query is the caller's prepared query, never a provider's echo of it:
	// providers may receive rewritten text such as appended site: operators
	// for domain filters.
	Query   string          `json:"query"`
	Results []*SearchResult `json:"results"`
}

func (s *SearchResponse) ValidateFor(request *SearchRequest) error {
	prepared, err := request.Prepare()
	if err != nil {
		return err
	}
	if s == nil {
		return ErrMissingSearchResponse
	}
	if s.Query != prepared.Query {
		return fmt.Errorf("%w: query differs from the prepared request", ErrInvalidSearchResponse)
	}
	if prepared.MaxResults > 0 && len(s.Results) > prepared.MaxResults {
		return fmt.Errorf("%w: returned %d results, maximum is %d", ErrInvalidSearchResponse, len(s.Results), prepared.MaxResults)
	}
	for index, result := range s.Results {
		if err := result.Validate(); err != nil {
			return fmt.Errorf("web: result %d: %w", index, err)
		}
	}
	return nil
}

// Searcher is the provider boundary behind the model-facing search tool.
// Authentication, endpoint selection, and provider defaults are fixed by the
// implementation. Implementations must be safe for concurrent use because
// search tools advertise parallel calls.
type Searcher interface {
	// Search performs one request without mutating or retaining it and
	// transfers ownership of the response to the caller. Implementations must
	// honor ctx, preserve provider error causes, and reject unsupported
	// explicit fields instead of ignoring them.
	Search(ctx context.Context, request *SearchRequest) (*SearchResponse, error)
}

func (SearchRequest) JSONSchemaConstraints() jsonschema.Constraints {
	return jsonschema.Constraints{Properties: map[string]jsonschema.Constraints{
		"max_results":     {Minimum: new(float64(1)), Maximum: new(float64(maxSearchResults))},
		"allowed_domains": {MaxItems: new(uint64(maxDomainFilters))},
		"blocked_domains": {MaxItems: new(uint64(maxDomainFilters))},
	}}
}
