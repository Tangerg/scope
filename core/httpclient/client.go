// Package httpclient bounds an API client's requests to its configured origin.
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
}

// New copies the client value before applying origin restrictions, so SDK
// initialization and redirect policy changes cannot mutate the host's client.
// The host retains ownership of the shared transport and cookie jar. Its
// redirect policy runs before the final destination and authority checks.
func New(_ context.Context, config Config) (*http.Client, error) {
	origin, err := parseOrigin(config.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("httpclient: invalid API endpoint: %w", err)
	}
	owned := http.Client{}
	if config.HTTPClient != nil {
		owned = *config.HTTPClient
	}
	origin.restrict(&owned)
	return &owned, nil
}
