package mistral

import (
	"cmp"
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/go-resty/resty/v2"

	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/httpclient"
	corevector "github.com/Tangerg/scope/core/vector"
)

type apiConfig struct {
	APIKey     string
	BaseURL    string
	HTTPClient *http.Client
}

func (a apiConfig) validate() error {
	if a.APIKey == "" {
		return errors.New("mistral: API key is required")
	}
	return nil
}

type api struct {
	http *resty.Client
}

func (a *api) embedding(ctx context.Context, request *embeddingRequest) (*embeddingResponse, error) {
	var result embeddingResponse
	response, err := a.http.R().SetContext(ctx).SetBody(request).SetResult(&result).Post("/embeddings")
	if err != nil {
		if errors.Is(err, corevector.ErrInvalidCoordinates) {
			return nil, fmt.Errorf("%w: mistral: decode embedding coordinates: %w", embedding.ErrInvalidResponse, err)
		}
		return nil, fmt.Errorf("mistral: embedding request: %w", err)
	}
	if !response.IsSuccess() {
		return nil, responseError("embeddings", response)
	}
	return &result, nil
}

func newAPI(ctx context.Context, config apiConfig) (*api, error) {
	if err := config.validate(); err != nil {
		return nil, err
	}
	baseURL := cmp.Or(config.BaseURL, DefaultBaseURL)
	boundClient, err := httpclient.New(ctx, httpclient.Config{HTTPClient: config.HTTPClient, BaseURL: baseURL})
	if err != nil {
		return nil, err
	}
	client := resty.NewWithClient(boundClient).SetResponseBodyLimit(httpclient.DefaultMaxJSONResponseBytes)
	client.JSONMarshal = func(value any) ([]byte, error) { return jsonv2.Marshal(value) }
	client.JSONUnmarshal = func(data []byte, value any) error { return jsonv2.Unmarshal(data, value) }
	client.
		SetBaseURL(baseURL).
		SetAuthToken(config.APIKey).
		SetHeader("Content-Type", "application/json")
	return &api{http: client}, nil
}

type moderationRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type moderationResponse struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Results []struct {
		Categories     map[string]*bool    `json:"categories"`
		CategoryScores map[string]*float64 `json:"category_scores"`
	} `json:"results"`
}

func (a *api) moderation(ctx context.Context, req *moderationRequest) (*moderationResponse, error) {
	if req == nil {
		return nil, errors.New("mistral: request must not be nil")
	}
	var out moderationResponse
	resp, err := a.http.R().SetContext(ctx).SetBody(req).SetResult(&out).Post("/moderations")
	if err != nil {
		return nil, fmt.Errorf("mistral: request failed: %w", err)
	}
	if !resp.IsSuccess() {
		return nil, fmt.Errorf("mistral: %w", httpclient.NewResponseError(resp.StatusCode(), resp.Header()))
	}
	return &out, nil
}

func (a *api) chatCompletionStream(ctx context.Context, request *chatCompletionRequest) (io.ReadCloser, error) {
	response, err := a.http.R().
		SetContext(ctx).
		SetBody(request).
		SetDoNotParseResponse(true).
		Post("/chat/completions")
	if err != nil {
		return nil, fmt.Errorf("mistral: chat completion stream request: %w", err)
	}
	body := response.RawBody()
	if response.IsSuccess() {
		return body, nil
	}
	if body != nil {
		_ = body.Close()
	}
	return nil, responseError("chat completion stream", response)
}

func responseError(operation string, response *resty.Response) error {
	return fmt.Errorf("mistral: %s: %w", operation, httpclient.NewResponseError(response.StatusCode(), response.Header()))
}
