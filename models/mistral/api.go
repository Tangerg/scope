package mistral

import (
	"cmp"
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/go-resty/resty/v2"
)

const maximumErrorResponseBytes = 1 << 20

// API is the authenticated transport for Mistral's native endpoints.
type apiConfig struct {
	APIKey     string
	BaseURL    string
	HTTPClient *http.Client
}

// APIError reports a non-successful Mistral HTTP response.
type APIError struct {
	Operation  string
	StatusCode int
	RequestID  string
	Message    string
}

func (a *APIError) Error() string {
	if a == nil {
		return "mistral: nil API error"
	}
	detail := fmt.Sprintf("mistral: %s returned HTTP %d", a.Operation, a.StatusCode)
	if a.Message != "" {
		detail += ": " + a.Message
	}
	if a.RequestID != "" {
		detail += " (request_id=" + a.RequestID + ")"
	}
	return detail
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

func newAPI(config apiConfig) (*api, error) {
	if err := config.validate(); err != nil {
		return nil, err
	}
	client := resty.New()
	if config.HTTPClient != nil {
		client = resty.NewWithClient(config.HTTPClient)
	}
	client.
		SetBaseURL(cmp.Or(config.BaseURL, DefaultBaseURL)).
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
		Categories     map[string]bool    `json:"categories"`
		CategoryScores map[string]float64 `json:"category_scores"`
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
		return nil, fmt.Errorf("mistral: http %d: %s", resp.StatusCode(), resp.String())
	}
	return &out, nil
}

func (a *api) chatCompletionStream(ctx context.Context, request *chatCompletionRequest) (io.ReadCloser, error) {
	if a == nil || a.http == nil {
		return nil, errors.New("mistral: nil API")
	}
	if request == nil {
		return nil, errors.New("mistral: chat completion request must not be nil")
	}
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
	defer body.Close()
	payload, readErr := io.ReadAll(io.LimitReader(body, maximumErrorResponseBytes))
	if readErr != nil {
		return nil, fmt.Errorf("mistral: read chat completion stream error: %w", readErr)
	}
	return nil, newAPIError("chat completion stream", response.StatusCode(), response.Header(), payload)
}

func newAPIError(operation string, statusCode int, header http.Header, body []byte) error {
	requestID := header.Get("request-id")
	if requestID == "" {
		requestID = header.Get("x-request-id")
	}
	message := strings.TrimSpace(string(body))
	var payload struct {
		Message string `json:"message"`
		Detail  any    `json:"detail"`
	}
	if jsonv2.Unmarshal(body, &payload) == nil {
		switch {
		case payload.Message != "":
			message = payload.Message
		case payload.Detail != nil:
			if encoded, err := jsonv2.Marshal(payload.Detail); err == nil {
				message = string(encoded)
			}
		}
	}
	return &APIError{
		Operation:  operation,
		StatusCode: statusCode,
		RequestID:  requestID,
		Message:    message,
	}
}
