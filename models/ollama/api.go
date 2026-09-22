package ollama

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

const (
	maxNativeStreamFrameBytes = 8 << 20
	maxNativeResponseBytes    = 64 << 20
	maxNativeErrorBytes       = 64 << 10
)

// APIConfig configures the Ollama native client. Unlike cloud
// providers, Ollama runs locally so the typical config is just the
// BaseURL of the daemon (default: http://127.0.0.1:11434). There is no
// API key.
type apiConfig struct {
	// BaseURL points at the Ollama daemon. Empty falls back to
	// [DefaultBaseURL]. Pass an env value like
	// "https://ollama.internal:11434" for remote setups.
	BaseURL string

	// HTTPClient lets callers thread their own client through. nil
	// falls back to [http.DefaultClient].
	HTTPClient *http.Client
}

func (a apiConfig) validate() error {
	return nil
}

// api is the narrow client mechanism for the two native endpoints this adapter
// owns. It deliberately does not import Ollama's daemon module.
type api struct {
	baseURL    *url.URL
	httpClient *http.Client
}

func newAPI(config apiConfig) (*api, error) {
	if err := config.validate(); err != nil {
		return nil, err
	}

	u, err := url.Parse(cmp.Or(config.BaseURL, DefaultBaseURL))
	if err != nil {
		return nil, errors.New("ollama: BaseURL must be a valid URL")
	}

	httpClient := config.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}

	return &api{baseURL: u, httpClient: httpClient}, nil
}

// chat decodes Ollama's newline-delimited response for both streaming and
// non-streaming requests. With stream=false the daemon returns one frame.
func (a *api) chat(ctx context.Context, req *nativeChatRequest, fn func(nativeChatResponse) error) error {
	if req == nil {
		return errors.New("ollama: request must not be nil")
	}
	response, err := a.request(ctx, "/api/chat", req, "application/x-ndjson")
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode >= http.StatusBadRequest {
		body, readErr := io.ReadAll(io.LimitReader(response.Body, maxNativeErrorBytes+1))
		if readErr != nil {
			return readErr
		}
		if len(body) > maxNativeErrorBytes {
			body = body[:maxNativeErrorBytes]
		}
		return nativeResponseError(response, body)
	}

	decoder := jsontext.NewDecoder(response.Body)
	for {
		var frame nativeChatResponse
		if err := jsonv2.UnmarshalDecode(decoder, &frame); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("ollama: decode native chat stream: %w", err)
		}
		if len(frame.raw) > maxNativeStreamFrameBytes {
			return fmt.Errorf("ollama: native stream frame exceeds %d bytes", maxNativeStreamFrameBytes)
		}
		if frame.Error != "" {
			return errors.New(frame.Error)
		}
		if err := fn(frame); err != nil {
			return err
		}
	}
}

func (a *api) embed(ctx context.Context, req *nativeEmbedRequest) (*nativeEmbedResponse, error) {
	if req == nil {
		return nil, errors.New("ollama: request must not be nil")
	}
	var response nativeEmbedResponse
	if err := a.call(ctx, "/api/embed", req, &response); err != nil {
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
	if err := nativeResponseError(response, body); err != nil {
		return err
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
	return a.httpClient.Do(request)
}

type nativeStatusError struct {
	statusCode int
	status     string
	message    string
}

func (n nativeStatusError) Error() string {
	switch {
	case n.status != "" && n.message != "":
		return n.status + ": " + n.message
	case n.status != "":
		return n.status
	case n.statusCode != 0 && n.message != "":
		return fmt.Sprintf("%d: %s", n.statusCode, n.message)
	case n.statusCode != 0:
		return fmt.Sprintf("ollama: HTTP status %d", n.statusCode)
	case n.message != "":
		return n.message
	default:
		return "ollama: request failed"
	}
}

func nativeResponseError(response *http.Response, body []byte) error {
	if response.StatusCode < http.StatusBadRequest {
		return nil
	}
	var providerFailure struct {
		Error string `json:"error"`
	}
	if err := jsonv2.Unmarshal(body, &providerFailure); err != nil || providerFailure.Error == "" {
		providerFailure.Error = string(body)
	}
	return nativeStatusError{
		statusCode: response.StatusCode,
		status:     response.Status,
		message:    providerFailure.Error,
	}
}
