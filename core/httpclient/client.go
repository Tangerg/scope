// Package httpclient bounds an API client's requests to its configured origin
// and owns the URL shapes adapters use to configure and address an API: base
// URLs, origins, and path segments.
package httpclient

import (
	"context"
	"fmt"
	"net/http"
)

type Config struct {
	// A nil Transport, including a typed nil, uses http.DefaultTransport.
	HTTPClient *http.Client
	BaseURL    string
	// RejectRedirects binds resource-scoped requests to the original URL, even
	// when a same-origin server redirects to a different tenant or resource.
	RejectRedirects bool
}

// New copies the client value before applying origin restrictions, so SDK
// initialization and redirect policy changes cannot mutate the host's client.
// The host retains ownership of the shared transport and cookie jar. Its
// redirect policy runs before the final destination and authority checks.
func New(_ context.Context, config Config) (*http.Client, error) {
	endpoint, err := ParseBaseURL(config.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("httpclient: invalid API endpoint: %w", err)
	}
	origin, err := NewOrigin(endpoint)
	if err != nil {
		return nil, fmt.Errorf("httpclient: invalid API endpoint: %w", err)
	}
	owned := copyClient(config.HTTPClient)
	restriction{allowed: Origins{origin}, rejectRedirects: config.RejectRedirects}.apply(owned)
	return owned, nil
}
