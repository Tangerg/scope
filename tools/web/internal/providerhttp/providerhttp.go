// Package providerhttp owns the JSON exchange shared by the web providers, so
// transport, status, and decoding failures have one definition.
package providerhttp

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"net/http"

	"github.com/go-resty/resty/v2"
)

const mediaTypeJSON = "application/json"

var errEmptyBody = errors.New("response body is empty")

// NewClient keeps the host's [http.Client] so it retains ownership of timeouts,
// proxying, and transport instrumentation.
func NewClient(httpClient *http.Client, baseURL string) *resty.Client {
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	client := resty.NewWithClient(httpClient).SetBaseURL(baseURL)
	client.JSONMarshal = func(value any) ([]byte, error) { return jsonv2.Marshal(value) }
	client.JSONUnmarshal = func(data []byte, value any) error { return jsonv2.Unmarshal(data, value) }
	return client
}

// Execute sends request and decodes its successful JSON body into result.
//
// resty skips decoding, without reporting an error, for a 204 response or a
// non-JSON Content-Type. Forcing the media type and requiring a body turns
// those responses into failures instead of empty provider results.
func Execute(request *resty.Request, method string, path string, result any) (*resty.Response, error) {
	response, err := request.ForceContentType(mediaTypeJSON).SetResult(result).Execute(method, path)
	if err != nil {
		return nil, err
	}
	if !response.IsSuccess() {
		return nil, fmt.Errorf("HTTP %d: %s", response.StatusCode(), response.String())
	}
	if len(response.Body()) == 0 {
		return nil, fmt.Errorf("HTTP %d: %w", response.StatusCode(), errEmptyBody)
	}
	return response, nil
}
