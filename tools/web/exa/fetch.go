package exa

import (
	"errors"
	"fmt"

	"github.com/Tangerg/scope/tools/web"
)

var _ web.Fetcher = (*Client)(nil)

type fetchTextOptions struct {
	IncludeHTMLTags bool `json:"includeHtmlTags,omitzero"`
}

type fetchRequest struct {
	URLs []string         `json:"urls,omitempty"`
	Text fetchTextOptions `json:"text"`
	// An explicit zero forces a fresh crawl; a cached page ignores Text options.
	MaxAgeHours int `json:"maxAgeHours"`
}

type fetchResult struct {
	URL  string  `json:"url"`
	Text *string `json:"text,omitzero"`
}

type fetchResponse struct {
	Results  []*fetchResult `json:"results"`
	Statuses []*fetchStatus `json:"statuses"`
}

func (f *fetchResponse) content(requestedURL string) (string, error) {
	if len(f.Statuses) > 1 {
		return "", fmt.Errorf("exa: %w: one requested URL has multiple statuses", web.ErrInvalidFetchResponse)
	}
	if len(f.Statuses) == 1 {
		if err := f.Statuses[0].validateFor(requestedURL); err != nil {
			return "", err
		}
	}
	if len(f.Results) != 1 || f.Results[0] == nil || f.Results[0].Text == nil {
		return "", fmt.Errorf("exa: %w: one requested URL requires one text result", web.ErrInvalidFetchResponse)
	}
	result := f.Results[0]
	if err := (&web.FetchRequest{URL: result.URL}).Validate(); err != nil {
		return "", fmt.Errorf("exa: %w: fetched result has no valid URL: %w", web.ErrInvalidFetchResponse, err)
	}
	// Status IDs bind the request; result URLs may name redirect destinations.
	return *result.Text, nil
}

type fetchState string

const (
	fetchSucceeded fetchState = "success"
	fetchFailed    fetchState = "error"
)

type fetchError struct {
	Tag            string `json:"tag"`
	HTTPStatusCode int    `json:"httpStatusCode"`
}

type fetchStatus struct {
	ID     string      `json:"id"`
	Status fetchState  `json:"status"`
	Error  *fetchError `json:"error"`
}

func (f *fetchStatus) validateFor(requestedURL string) error {
	if f == nil || f.ID != requestedURL {
		return fmt.Errorf("exa: %w: status has no requested URL identity", web.ErrInvalidFetchResponse)
	}
	switch f.Status {
	case fetchSucceeded:
		if f.Error != nil {
			return fmt.Errorf("exa: %w: successful status contains an error", web.ErrInvalidFetchResponse)
		}
		return nil
	case fetchFailed:
		if f.Error == nil {
			return errors.New("exa: fetch response reported failure")
		}
		return fmt.Errorf("exa: fetch response reported %s (HTTP %d)", f.Error.Tag, f.Error.HTTPStatusCode)
	default:
		return fmt.Errorf("exa: %w: unknown fetch status %q", web.ErrInvalidFetchResponse, f.Status)
	}
}
