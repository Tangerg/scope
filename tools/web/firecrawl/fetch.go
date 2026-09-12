package firecrawl

import (
	"errors"

	"github.com/Tangerg/scope/tools/web"
)

var _ web.Fetcher = (*Client)(nil)

type fetchFormat struct {
	Type string `json:"type"`
}

type fetchRequest struct {
	URL             string        `json:"url"`
	Formats         []fetchFormat `json:"formats"`
	OnlyMainContent bool          `json:"onlyMainContent"`
}

func (f *fetchRequest) validate() error {
	if f == nil {
		return errors.New("firecrawl: fetch request must not be nil")
	}
	if f.URL == "" {
		return errors.New("firecrawl: fetch URL must not be empty")
	}
	return nil
}

type fetchResponseData struct {
	Markdown string  `json:"markdown,omitempty"`
	HTML     *string `json:"html,omitempty"`
}

type fetchResponse struct {
	Success bool              `json:"success"`
	Data    fetchResponseData `json:"data"`
}
