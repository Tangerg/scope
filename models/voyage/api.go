package voyage

import (
	"cmp"
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"net/http"

	"github.com/go-resty/resty/v2"

	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/httpclient"
	corevector "github.com/Tangerg/scope/core/vector"
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
	client.SetJSONMarshaler(func(value any) ([]byte, error) { return jsonv2.Marshal(value) }).
		SetJSONUnmarshaler(func(data []byte, value any) error { return jsonv2.Unmarshal(data, value) })
	client.SetBaseURL(baseURL).
		SetAuthToken(config.APIKey).
		SetHeader("Content-Type", "application/json")

	return &api{http: client}, nil
}

type embeddingRequest struct {
	Input           []string            `json:"input"`
	Model           string              `json:"model"`
	InputType       *embeddingInputType `json:"input_type,omitzero"`
	Truncation      *bool               `json:"truncation,omitzero"`
	OutputDimension *int64              `json:"output_dimension,omitzero"`
	OutputDtype     string              `json:"output_dtype,omitempty"`
	EncodingFormat  string              `json:"encoding_format,omitempty"`
}

func (e *embeddingRequest) UnmarshalJSON(data []byte) error {
	type wireRequest embeddingRequest
	var decoded *wireRequest
	if err := jsonv2.Unmarshal(data, &decoded, jsonv2.RejectUnknownMembers(true)); err != nil {
		return fmt.Errorf("voyage: embedding options: %w", err)
	}
	if decoded == nil {
		return errors.New("voyage: embedding options must be an object")
	}
	*e = embeddingRequest(*decoded)
	return nil
}

type embeddingResponse struct {
	Object string `json:"object"`
	Data   []struct {
		Object    string                          `json:"object"`
		Embedding corevector.Coordinates[float64] `json:"embedding"`
		Index     *int64                          `json:"index"`
	} `json:"data"`
	Model string `json:"model"`
	Usage struct {
		TotalTokens *int64 `json:"total_tokens"`
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
		Index          *int     `json:"index"`
		RelevanceScore *float64 `json:"relevance_score"`
	} `json:"data"`
	Model string `json:"model"`
	Usage struct {
		TotalTokens *int64 `json:"total_tokens"`
	} `json:"usage"`
}

func (a *api) embedding(ctx context.Context, req *embeddingRequest) (*embeddingResponse, error) {
	if req == nil {
		return nil, errors.New("voyage: request must not be nil")
	}

	out, err := a.postJSON[embeddingResponse](ctx, embeddingEndpointPath, req)
	if errors.Is(err, corevector.ErrInvalidCoordinates) {
		return nil, fmt.Errorf("%w: voyage: decode embedding coordinates: %w", embedding.ErrInvalidResponse, err)
	}
	return out, err
}

func (a *api) rerank(ctx context.Context, req *rerankRequest) (*rerankResponse, error) {
	if req == nil {
		return nil, errors.New("voyage: request must not be nil")
	}
	return a.postJSON[rerankResponse](ctx, rerankEndpointPath, req)
}

// postJSON sends one JSON request and decodes a successful JSON reply; any
// other status is a response error that keeps the status and headers.
func (a *api) postJSON[R any](ctx context.Context, path string, body any) (*R, error) {
	var out R
	resp, err := a.http.R().SetContext(ctx).SetBody(body).SetResult(&out).Post(path)
	if err != nil {
		return nil, fmt.Errorf("voyage: request failed: %w", err)
	}
	if !resp.IsSuccess() {
		return nil, fmt.Errorf("voyage: %w", httpclient.NewResponseError(resp.StatusCode(), resp.Header()))
	}
	return &out, nil
}
