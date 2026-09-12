package tavily

import (
	"errors"

	"github.com/Tangerg/scope/tools/web"
)

var _ web.Fetcher = (*Client)(nil)

type fetchRequest struct {
	URLs         []string `json:"urls"`
	ExtractDepth string   `json:"extract_depth,omitempty"`
	Format       string   `json:"format,omitempty"`
}

func (f *fetchRequest) validate() error {
	if f == nil {
		return errors.New("tavily: fetch request must not be nil")
	}
	if len(f.URLs) == 0 {
		return errors.New("tavily: fetch URLs must not be empty")
	}
	return nil
}

type fetchResult struct {
	RawContent string `json:"raw_content"`
}

type failedFetchResult struct {
	Error string `json:"error"`
}

type fetchResponse struct {
	Results       []*fetchResult       `json:"results"`
	FailedResults []*failedFetchResult `json:"failed_results"`
}
