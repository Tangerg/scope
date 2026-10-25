package jina

import (
	"fmt"

	"github.com/Tangerg/scope/tools/web"
)

var _ web.Fetcher = (*Client)(nil)

type fetchRequest struct {
	URL string `json:"url"`
}

// Reader selects a different property for each requested format. Pointers keep
// a valid empty payload distinct from a missing one without guessing a fallback.
type fetchResponseData struct {
	Content *string `json:"content"`
	HTML    *string `json:"html"`
	Text    *string `json:"text"`
}

type fetchResponse struct {
	Data fetchResponseData `json:"data"`
}

func (f *fetchResponse) toFetchResponse(format web.ContentFormat) (*web.FetchResponse, error) {
	var content *string
	switch format {
	case web.FormatMarkdown:
		content = f.Data.Content
	case web.FormatHTML:
		content = f.Data.HTML
	case web.FormatText:
		content = f.Data.Text
	default:
		return nil, fmt.Errorf("jina: %w: %q", web.ErrUnsupportedFormat, format)
	}
	if content == nil {
		return nil, fmt.Errorf("jina: %w: response is missing the %s payload", web.ErrInvalidFetchResponse, format)
	}
	response := &web.FetchResponse{Content: *content, Format: format}
	if err := response.Validate(); err != nil {
		return nil, fmt.Errorf("jina: %w", err)
	}
	return response, nil
}
