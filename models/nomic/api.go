package nomic

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/go-resty/resty/v2"
)

type apiConfig struct {
	APIKey     string
	BaseURL    string
	HTTPClient *http.Client
}

func (a apiConfig) validate() error {
	if a.APIKey == "" {
		return errors.New("nomic: APIKey is required")
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

	var client *resty.Client
	if config.HTTPClient != nil {
		client = resty.NewWithClient(config.HTTPClient)
	} else {
		client = resty.New()
	}
	client.
		SetBaseURL(cmp.Or(config.BaseURL, DefaultBaseURL)).
		SetAuthToken(config.APIKey).
		SetHeader("Content-Type", "application/json").
		SetHeader("Accept", "application/json")

	return &api{http: client}, nil
}

type embeddingRequest struct {
	Model          string   `json:"model"`
	Texts          []string `json:"texts"`
	TaskType       string   `json:"task_type,omitempty"`
	Dimensionality *int64   `json:"dimensionality,omitzero"`
	LongTextMode   string   `json:"long_text_mode,omitempty"`
}

type embeddingResponse struct {
	Embeddings [][]float64 `json:"embeddings"`
	Model      string      `json:"model"`
	Usage      struct {
		PromptTokens int64 `json:"prompt_tokens"`
		TotalTokens  int64 `json:"total_tokens"`
	} `json:"usage"`
}

func (a *api) embedding(ctx context.Context, req *embeddingRequest) (*embeddingResponse, error) {
	if req == nil {
		return nil, errors.New("nomic: request must not be nil")
	}
	if len(req.Texts) == 0 {
		return nil, errors.New("nomic: texts must not be empty")
	}

	var out embeddingResponse
	resp, err := a.http.R().
		SetContext(ctx).
		SetBody(req).
		SetResult(&out).
		Post("/embedding/text")
	if err != nil {
		return nil, fmt.Errorf("nomic: request failed: %w", err)
	}
	if !resp.IsSuccess() {
		return nil, fmt.Errorf("nomic: http %d: %s", resp.StatusCode(), resp.String())
	}
	return &out, nil
}
