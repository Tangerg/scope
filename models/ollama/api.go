package ollama

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/httpclient"
	corevector "github.com/Tangerg/scope/core/vector"
)

const (
	maxNativeStreamFrameBytes = 8 << 20
	maxNativeResponseBytes    = 64 << 20
	maxNativeErrorBytes       = 64 << 10
)

// Ollama runs locally, so the typical config is just the BaseURL of the
// daemon (default: http://127.0.0.1:11434). There is no API key.
type apiConfig struct {
	// BaseURL points at the Ollama daemon. Empty falls back to
	// [DefaultBaseURL]. Pass an env value like
	// "https://ollama.internal:11434" for remote setups.
	BaseURL string

	// HTTPClient lets callers thread their own client through. nil
	// falls back to [http.DefaultClient].
	HTTPClient *http.Client
}

// api is the narrow client mechanism for the two native endpoints this adapter
// owns. It deliberately does not import Ollama's daemon module.
type api struct {
	baseURL    *url.URL
	httpClient *http.Client
}

func newAPI(ctx context.Context, config apiConfig) (*api, error) {
	u, err := url.Parse(cmp.Or(config.BaseURL, DefaultBaseURL))
	if err != nil {
		return nil, errors.New("ollama: BaseURL must be a valid URL")
	}

	httpClient, err := httpclient.New(ctx, httpclient.Config{HTTPClient: config.HTTPClient, BaseURL: u.String()})
	if err != nil {
		return nil, err
	}

	return &api{baseURL: u, httpClient: httpClient}, nil
}

// chat decodes Ollama's newline-delimited response for both streaming and
// non-streaming requests. With stream=false the daemon returns one frame.
func (a *api) chat(ctx context.Context, req nativeChatRequest, fn func(nativeChatResponse) error) (err error) {
	response, err := a.request(ctx, "/api/chat", req, "application/x-ndjson")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, response.Body.Close(), ctx.Err()) }()
	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 4096), maxNativeStreamFrameBytes+2)
	for scanner.Scan() {
		raw := scanner.Bytes()
		if len(raw) > maxNativeStreamFrameBytes {
			return fmt.Errorf("ollama: native stream frame exceeds %d bytes", maxNativeStreamFrameBytes)
		}
		if len(bytes.TrimSpace(raw)) == 0 {
			continue
		}
		var frame nativeChatResponse
		if err := jsonv2.Unmarshal(raw, &frame); err != nil {
			return fmt.Errorf("ollama: decode native chat stream: %w", err)
		}
		if frame.Error != "" {
			return errors.New(frame.Error)
		}
		if err := fn(frame); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("ollama: read native chat frame (maximum %d bytes): %w", maxNativeStreamFrameBytes, err)
	}
	return nil
}

func (a *api) embed(ctx context.Context, req nativeEmbedRequest) (*nativeEmbedResponse, error) {
	var response nativeEmbedResponse
	if err := a.call(ctx, "/api/embed", req, &response); err != nil {
		if errors.Is(err, corevector.ErrInvalidCoordinates) {
			return nil, fmt.Errorf("%w: ollama: decode embedding coordinates: %w", embedding.ErrInvalidResponse, err)
		}
		return nil, err
	}
	return &response, nil
}

func (a *api) call(ctx context.Context, path string, requestValue, responseValue any) error {
	response, err := a.request(ctx, path, requestValue, "application/json")
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxNativeResponseBytes+1))
	if err != nil {
		return err
	}
	if len(body) > maxNativeResponseBytes {
		return fmt.Errorf("ollama: response exceeds %d bytes", maxNativeResponseBytes)
	}
	if len(body) == 0 || responseValue == nil {
		return nil
	}
	return jsonv2.Unmarshal(body, responseValue)
}

func (a *api) request(
	ctx context.Context,
	path string,
	requestValue any,
	accept string,
) (*http.Response, error) {
	body, err := jsonv2.Marshal(requestValue)
	if err != nil {
		return nil, err
	}
	endpoint := a.baseURL.JoinPath(path)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", accept)
	request.Header.Set("User-Agent", "scope-ollama")
	response, err := a.httpClient.Do(request)
	if err != nil {
		return nil, err
	}
	if response.StatusCode >= http.StatusOK && response.StatusCode < http.StatusMultipleChoices {
		return response, nil
	}
	defer response.Body.Close()
	errorBody, readErr := io.ReadAll(io.LimitReader(response.Body, maxNativeErrorBytes+1))
	if len(errorBody) > maxNativeErrorBytes {
		errorBody = errorBody[:maxNativeErrorBytes]
	}
	return nil, errors.Join(statusError(response, errorBody), readErr)
}

// statusError keeps Ollama's own failure text beside the transport evidence:
// a local server's message, such as a missing model, is the actionable part.
func statusError(response *http.Response, body []byte) error {
	var providerFailure struct {
		Error string `json:"error"`
	}
	if err := jsonv2.Unmarshal(body, &providerFailure); err != nil || providerFailure.Error == "" {
		providerFailure.Error = string(body)
	}
	evidence := httpclient.NewResponseError(response.StatusCode, response.Header)
	if providerFailure.Error == "" {
		return evidence
	}
	return fmt.Errorf("%w: ollama: %s", evidence, providerFailure.Error)
}
