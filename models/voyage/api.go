package voyage

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
		return errors.New("voyage: APIKey is required")
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
	client.SetBaseURL(cmp.Or(config.BaseURL, DefaultBaseURL)).
		SetAuthToken(config.APIKey).
		SetHeader("Content-Type", "application/json")

	return &api{http: client}, nil
}

type embeddingRequest struct {
	Input           []string `json:"input"`
	Model           string   `json:"model"`
	InputType       string   `json:"input_type,omitempty"`
	Truncation      *bool    `json:"truncation,omitzero"`
	OutputDimension *int64   `json:"output_dimension,omitzero"`
	OutputDtype     string   `json:"output_dtype,omitempty"`
	EncodingFormat  string   `json:"encoding_format,omitempty"`
}

type embeddingResponse struct {
	Object string `json:"object"`
	Data   []struct {
		Object    string    `json:"object"`
		Embedding []float64 `json:"embedding"`
		Index     int64     `json:"index"`
	} `json:"data"`
	Model string `json:"model"`
	Usage struct {
		TotalTokens int64 `json:"total_tokens"`
	} `json:"usage"`
}

type rerankRequest struct {
	Query           string   `json:"query"`
	Documents       []string `json:"documents"`
	Model           string   `json:"model"`
	TopK            *int     `json:"top_k,omitzero"`
	ReturnDocuments bool     `json:"return_documents"`
	Truncation      *bool    `json:"truncation,omitzero"`
}

type rerankResponse struct {
	Object string `json:"object"`
	Data   []struct {
		Index          int     `json:"index"`
		RelevanceScore float64 `json:"relevance_score"`
	} `json:"data"`
	Model string `json:"model"`
	Usage struct {
		TotalTokens int64 `json:"total_tokens"`
	} `json:"usage"`
}

func (a *api) embedding(ctx context.Context, req *embeddingRequest) (*embeddingResponse, error) {
	if req == nil {
		return nil, errors.New("voyage: request must not be nil")
	}

	var out embeddingResponse
	resp, err := a.http.R().
		SetContext(ctx).
		SetBody(req).
		SetResult(&out).
		Post(embeddingEndpointPath)
	if err != nil {
		return nil, fmt.Errorf("voyage: request failed: %w", err)
	}
	if !resp.IsSuccess() {
		return nil, fmt.Errorf("voyage: http %d: %s", resp.StatusCode(), resp.String())
	}
	return &out, nil
}

func (a *api) rerank(ctx context.Context, req *rerankRequest) (*rerankResponse, error) {
	if req == nil {
		return nil, errors.New("voyage: request must not be nil")
	}
	var out rerankResponse
	resp, err := a.http.R().
		SetContext(ctx).
		SetBody(req).
		SetResult(&out).
		Post(rerankEndpointPath)
	if err != nil {
		return nil, fmt.Errorf("voyage: request failed: %w", err)
	}
	if !resp.IsSuccess() {
		return nil, fmt.Errorf("voyage: http %d: %s", resp.StatusCode(), resp.String())
	}
	return &out, nil
}
