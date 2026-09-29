package exa

import "github.com/Tangerg/scope/tools/web"

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
	Text *string `json:"text,omitzero"`
}

type fetchResponse struct {
	Results []*fetchResult `json:"results"`
}
