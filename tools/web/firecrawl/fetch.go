package firecrawl

import "github.com/Tangerg/scope/tools/web"

var _ web.Fetcher = (*Client)(nil)

type fetchFormat struct {
	Type string `json:"type"`
}

type fetchRequest struct {
	URL             string        `json:"url"`
	Formats         []fetchFormat `json:"formats"`
	OnlyMainContent bool          `json:"onlyMainContent"`
}

type fetchResponseData struct {
	Markdown *string `json:"markdown,omitzero"`
	HTML     *string `json:"html,omitzero"`
}

func (f fetchResponseData) content(format web.ContentFormat) *string {
	if format == web.FormatHTML {
		return f.HTML
	}
	return f.Markdown
}

type fetchResponse struct {
	Success bool              `json:"success"`
	Data    fetchResponseData `json:"data"`
}
