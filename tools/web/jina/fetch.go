package jina

import (
	"errors"

	"github.com/Tangerg/scope/tools/web"
)

var _ web.Fetcher = (*Client)(nil)

type fetchRequest struct {
	URL          string `json:"url"`
	ReturnFormat string `json:"-"`
}

func (f *fetchRequest) validate() error {
	if f == nil {
		return errors.New("jina: fetch request must not be nil")
	}
	if f.URL == "" {
		return errors.New("jina: fetch URL must not be empty")
	}
	return nil
}

type fetchResponseData struct {
	Content string `json:"content"`
}

type fetchResponse struct {
	Data fetchResponseData `json:"data"`
}
