package jina

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/go-resty/resty/v2"
)

const (
	embeddingEndpointPath = "/embeddings"
	rerankEndpointPath    = "/rerank"
)

type apiConfig struct {
	APIKey     string
	BaseURL    string
	HTTPClient *http.Client
}

func (a apiConfig) validate() error {
	if a.APIKey == "" {
		return errors.New("jina: APIKey is required")
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
	Model         string   `json:"model"`
	Input         []string `json:"input"`
	Task          string   `json:"task,omitempty"`
	LateChunking  *bool    `json:"late_chunking,omitzero"`
	Dimensions    *int64   `json:"dimensions,omitzero"`
	Truncate      *bool    `json:"truncate,omitzero"`
	EmbeddingType string   `json:"embedding_type,omitempty"`
	Normalized    *bool    `json:"normalized,omitzero"`
}

type embeddingResponse struct {
	Object string `json:"object"`
	Model  string `json:"model"`
	Data   []struct {
		Object    string    `json:"object"`
		Index     int64     `json:"index"`
		Embedding []float64 `json:"embedding"`
	} `json:"data"`
	Usage struct {
		TotalTokens  int64 `json:"total_tokens"`
		PromptTokens int64 `json:"prompt_tokens"`
	} `json:"usage"`
}

type rerankRequest struct {
	Model           string   `json:"model"`
	Query           string   `json:"query"`
	Documents       []string `json:"documents"`
	TopN            *int     `json:"top_n,omitzero"`
	ReturnDocuments bool     `json:"return_documents"`
}

type rerankResponse struct {
	Model   string `json:"model"`
	Results []struct {
		Index          int     `json:"index"`
		RelevanceScore float64 `json:"relevance_score"`
	} `json:"results"`
	Usage struct {
		TotalTokens int64 `json:"total_tokens"`
	} `json:"usage"`
}

func (a *api) embedding(ctx context.Context, req *embeddingRequest) (*embeddingResponse, error) {
	if req == nil {
		return nil, errors.New("jina: request must not be nil")
	}
	if len(req.Input) == 0 {
		return nil, errors.New("jina: input must not be empty")
	}

	var out embeddingResponse
	resp, err := a.http.R().
		SetContext(ctx).
		SetBody(req).
		SetResult(&out).
		Post(embeddingEndpointPath)
	if err != nil {
		return nil, fmt.Errorf("jina: request failed: %w", err)
	}
	if !resp.IsSuccess() {
		return nil, fmt.Errorf("jina: http %d: %s", resp.StatusCode(), resp.String())
	}
	return &out, nil
}

func (a *api) rerank(ctx context.Context, req *rerankRequest) (*rerankResponse, error) {
	if req == nil {
		return nil, errors.New("jina: request must not be nil")
	}
	var out rerankResponse
	resp, err := a.http.R().
		SetContext(ctx).
		SetBody(req).
		SetResult(&out).
		Post(rerankEndpointPath)
	if err != nil {
		return nil, fmt.Errorf("jina: request failed: %w", err)
	}
	if !resp.IsSuccess() {
		return nil, fmt.Errorf("jina: http %d: %s", resp.StatusCode(), resp.String())
	}
	return &out, nil
}
