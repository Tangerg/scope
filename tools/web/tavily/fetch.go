package tavily

import (
	"errors"
	"fmt"

	"github.com/Tangerg/scope/tools/web"
)

var _ web.Fetcher = (*Client)(nil)

type fetchRequest struct {
	URLs         []string `json:"urls"`
	ExtractDepth string   `json:"extract_depth,omitempty"`
	Format       string   `json:"format,omitempty"`
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

func (f *fetchResponse) content() (string, error) {
	if len(f.Results) > 0 && f.Results[0] != nil {
		return f.Results[0].RawContent, nil
	}
	if len(f.FailedResults) > 0 && f.FailedResults[0] != nil {
		return "", fmt.Errorf("tavily: fetch response reported failure: %s", f.FailedResults[0].Error)
	}
	return "", errors.New("tavily: fetch response contains no result")
}
