package tavily

import (
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
	URL        string  `json:"url"`
	RawContent *string `json:"raw_content"`
}

type failedFetchResult struct {
	URL   string `json:"url"`
	Error string `json:"error"`
}

type fetchResponse struct {
	Results       []*fetchResult       `json:"results"`
	FailedResults []*failedFetchResult `json:"failed_results"`
}

func (f *fetchResponse) content(requestedURL string) (string, error) {
	if len(f.FailedResults) > 0 {
		failure := f.FailedResults[0]
		if failure == nil || failure.URL != requestedURL {
			return "", fmt.Errorf("tavily: %w: failure has no requested URL identity", web.ErrInvalidFetchResponse)
		}
		return "", fmt.Errorf("tavily: fetch response reported failure: %s", failure.Error)
	}
	if len(f.Results) != 1 || f.Results[0] == nil {
		return "", fmt.Errorf("tavily: %w: one requested URL requires exactly one result", web.ErrInvalidFetchResponse)
	}
	result := f.Results[0]
	if result.RawContent == nil {
		return "", fmt.Errorf("tavily: %w: fetched result is missing raw_content", web.ErrInvalidFetchResponse)
	}
	if err := (&web.FetchRequest{URL: result.URL}).Validate(); err != nil {
		return "", fmt.Errorf("tavily: %w: fetched result has no valid URL: %w", web.ErrInvalidFetchResponse, err)
	}
	// A single successful result may name the canonical URL after a redirect.
	return *result.RawContent, nil
}
