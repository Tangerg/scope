package exa

import (
	"errors"

	"github.com/Tangerg/scope/tools/web"
)

var _ web.Fetcher = (*Client)(nil)

type fetchTextOptions struct {
	IncludeHTMLTags bool `json:"includeHtmlTags,omitempty"`
}

type fetchRequest struct {
	URLs []string         `json:"urls,omitempty"`
	Text fetchTextOptions `json:"text,omitzero"`
}

func (f *fetchRequest) validate() error {
	if f == nil {
		return errors.New("exa: fetch request must not be nil")
	}
	if len(f.URLs) == 0 {
		return errors.New("exa: fetch URLs must not be empty")
	}
	return nil
}

type fetchResult struct {
	Text string `json:"text,omitempty"`
}

type fetchResponse struct {
	Results []*fetchResult `json:"results"`
}
