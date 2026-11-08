package web

import (
	"errors"
	"fmt"
)

var (
	ErrMissingSearchRequest = errors.New("web: search request must not be nil")

	ErrEmptyQuery   = errors.New("web: search query must not be empty")
	ErrInvalidQuery = errors.New("web: invalid search query")

	ErrDomainsBothSides  = errors.New("web: allowed_domains and blocked_domains are mutually exclusive")
	ErrInvalidMaxResults = fmt.Errorf("web: max_results must be between 1 and %d when set", maxSearchResults)
	ErrTooManyDomains    = fmt.Errorf("web: at most %d allowed_domains or blocked_domains may be set", maxDomainFilters)
	ErrInvalidDomain     = errors.New("web: domain filter must be a bare hostname without scheme, port, path, query, or fragment")
	ErrInvalidRecency    = errors.New("web: recency must be hour, day, week, month, or year")

	ErrMissingSearcher       = errors.New("web: searcher is required")
	ErrMissingSearchResponse = errors.New("web: search response must not be nil")
	ErrInvalidSearchResponse = errors.New("web: search response is invalid")
	ErrUnsupportedFilter     = errors.New("web: search filter is not supported by provider")
)
