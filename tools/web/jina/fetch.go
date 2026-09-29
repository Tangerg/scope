package jina

import "github.com/Tangerg/scope/tools/web"

var _ web.Fetcher = (*Client)(nil)

type fetchRequest struct {
	URL string `json:"url"`
}

type fetchResponseData struct {
	Content *string `json:"content"`
}

type fetchResponse struct {
	Data fetchResponseData `json:"data"`
}
