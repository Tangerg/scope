package web

import (
	"context"
	"fmt"
	"net/url"
	"strings"
)

// ContentFormat selects the representation of fetched page content.
// Providers map this to their native format setting.
type ContentFormat string

const (
	// FormatMarkdown returns the page rendered to Markdown. This is
	// the default and usually the most LLM-friendly format.
	FormatMarkdown ContentFormat = "markdown"
	// FormatHTML returns the page's HTML (or a cleaned variant).
	FormatHTML ContentFormat = "html"
	// FormatText returns plain text — no markup, no structure.
	FormatText ContentFormat = "text"
)

func (c ContentFormat) Validate() error {
	switch c {
	case "", FormatMarkdown, FormatHTML, FormatText:
		return nil
	default:
		return ErrInvalidFormat
	}
}

func (c ContentFormat) Resolve() ContentFormat {
	if c == "" {
		return FormatMarkdown
	}
	return c
}

// FetchRequest is both the provider-neutral fetch contract and the
// LLM-facing argument shape.
type FetchRequest struct {
	// URL is the page to fetch. Required.
	URL string `json:"url" jsonschema:"minLength=1" jsonschema_description:"Absolute http(s) URL of the page to fetch."`

	// Format selects the response format. "" defaults to markdown.
	Format ContentFormat `json:"format,omitempty" jsonschema:"enum=markdown,enum=html,enum=text" jsonschema_description:"Content format: markdown (default and best for readable structure), html, or text."`
}

// Prepare returns a normalized and validated request without mutating f.
func (f *FetchRequest) Prepare() (*FetchRequest, error) {
	if f == nil {
		return nil, ErrMissingFetchRequest
	}
	prepared := *f
	prepared.URL = strings.TrimSpace(f.URL)
	prepared.Format = f.Format.Resolve()
	if err := prepared.Validate(); err != nil {
		return nil, err
	}
	return &prepared, nil
}

func (f *FetchRequest) Validate() error {
	if f == nil {
		return ErrMissingFetchRequest
	}
	trimmedURL := strings.TrimSpace(f.URL)
	if trimmedURL == "" {
		return ErrEmptyURL
	}
	parsed, err := url.Parse(trimmedURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return ErrInvalidURL
	}
	return f.Format.Validate()
}

// FetchResponse is the normalized scrape result. Used as both the SPI
// return type and the LLM-facing serialization shape.
type FetchResponse struct {
	Content string        `json:"content"`
	Format  ContentFormat `json:"format"`
}

func (f *FetchResponse) Validate() error {
	if f == nil {
		return ErrMissingFetchResponse
	}
	if f.Format == "" {
		return fmt.Errorf("%w: response format is empty", ErrInvalidFetchResponse)
	}
	if err := f.Format.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidFetchResponse, err)
	}
	return nil
}

// Fetcher is the provider boundary behind the model-facing page fetch tool.
// Network authority, authentication, redirects, and provider defaults are
// frozen in the implementation rather than supplied by model arguments.
// Implementations must support concurrent calls, including calls through
// other tools sharing the same backend; fetch tools advertise parallel use.
type Fetcher interface {
	// Fetch retrieves and renders exactly request.URL in the requested format
	// without mutating or retaining request. Implementations must honor ctx,
	// preserve network error causes, and transfer response ownership to the
	// caller.
	Fetch(ctx context.Context, request *FetchRequest) (*FetchResponse, error)
}
