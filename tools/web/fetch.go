package web

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"
)

type ContentFormat string

const (
	// FormatMarkdown is the default format.
	FormatMarkdown ContentFormat = "markdown"
	FormatHTML     ContentFormat = "html"
	FormatText     ContentFormat = "text"
)

func (c ContentFormat) Validate() error {
	switch c {
	case "", FormatMarkdown, FormatHTML, FormatText:
		return nil
	default:
		return ErrInvalidFormat
	}
}

func (c ContentFormat) Normalize() (ContentFormat, error) {
	if c == "" {
		c = FormatMarkdown
	}
	if err := c.Validate(); err != nil {
		return "", err
	}
	return c, nil
}

type FetchRequest struct {
	URL string `json:"url" jsonschema:"minLength=1" jsonschema_description:"Absolute http(s) URL of the page to fetch."`

	Format ContentFormat `json:"format,omitempty" jsonschema:"enum=markdown,enum=html,enum=text" jsonschema_description:"Content format: markdown (default and best for readable structure), html, or text."`
}

// Prepare returns a normalized and validated request without mutating f.
func (f *FetchRequest) Prepare() (*FetchRequest, error) {
	if f == nil {
		return nil, ErrMissingFetchRequest
	}
	prepared := *f
	prepared.URL = strings.TrimSpace(f.URL)
	format, err := f.Format.Normalize()
	if err != nil {
		return nil, err
	}
	prepared.Format = format
	if err := prepared.Validate(); err != nil {
		return nil, err
	}
	return &prepared, nil
}

func (f *FetchRequest) Validate() error {
	if f == nil {
		return ErrMissingFetchRequest
	}
	if strings.TrimSpace(f.URL) == "" {
		return ErrEmptyURL
	}
	if !isHTTPURL(f.URL) {
		return ErrInvalidURL
	}
	return f.Format.Validate()
}

func isHTTPURL(raw string) bool {
	if !utf8.ValidString(raw) {
		return false
	}
	parsed, err := url.Parse(strings.TrimSpace(raw))
	return err == nil && parsed.Hostname() != "" && (parsed.Scheme == "http" || parsed.Scheme == "https")
}

type FetchResponse struct {
	Content string        `json:"content"`
	Format  ContentFormat `json:"format"`
}

func (f *FetchResponse) Validate() error {
	if f == nil {
		return ErrMissingFetchResponse
	}
	if !utf8.ValidString(f.Content) {
		return fmt.Errorf("%w: content must be valid UTF-8", ErrInvalidFetchResponse)
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
// Network authority, authentication, redirects, and provider defaults are fixed
// by the implementation rather than supplied by model arguments.
// Implementations must be safe for concurrent use because fetch tools
// advertise parallel calls.
type Fetcher interface {
	// Fetch retrieves exactly request.URL in the requested format without
	// mutating or retaining request, and transfers ownership of the response to
	// the caller. Implementations must honor ctx and preserve network error
	// causes.
	Fetch(ctx context.Context, request *FetchRequest) (*FetchResponse, error)
}
