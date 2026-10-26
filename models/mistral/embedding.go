package mistral

import (
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"net/http"

	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/metadata"
)

const (
	EmbeddingRequestExtensionKey = "mistral/embedding_request"
	embeddingResponseIDKey       = "mistral/embedding_id"
	embeddingFloatEncoding       = "float"
)

type EmbeddingRequestOptions struct {
	Metadata metadata.Map `json:"metadata,omitzero"`
}

func (e *EmbeddingRequestOptions) UnmarshalJSON(data []byte) error {
	type wireOptions EmbeddingRequestOptions
	var decoded *wireOptions
	if err := jsonv2.Unmarshal(data, &decoded, jsonv2.RejectUnknownMembers(true)); err != nil {
		return fmt.Errorf("mistral: embedding options: %w", err)
	}
	if decoded == nil {
		return errors.New("mistral: embedding options must be an object")
	}
	*e = EmbeddingRequestOptions(*decoded)
	return nil
}

type EmbeddingModelConfig struct {
	APIKey         string
	DefaultOptions embedding.Options
	BaseURL        string
	HTTPClient     *http.Client
}

func (e EmbeddingModelConfig) Validate() error {
	if e.APIKey == "" {
		return errors.New("mistral: API key is required")
	}
	if e.DefaultOptions.Model == "" {
		return errors.New("mistral: default model is required")
	}
	if err := e.DefaultOptions.Validate(); err != nil {
		return err
	}
	return nil
}

var _ embedding.Model = (*EmbeddingModel)(nil)

// Mistral owns its embedding wire vocabulary: Core dimensions project to
// output_dimension, while the float representation stays fixed for Core vectors.
type EmbeddingModel struct {
	api      *api
	defaults embedding.Options
}

func NewEmbeddingModel(ctx context.Context, config EmbeddingModelConfig) (*EmbeddingModel, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	api, err := newAPI(ctx, apiConfig{
		APIKey: config.APIKey, BaseURL: config.BaseURL, HTTPClient: config.HTTPClient,
	})
	if err != nil {
		return nil, err
	}
	return &EmbeddingModel{api: api, defaults: config.DefaultOptions.Clone()}, nil
}

func (e *EmbeddingModel) Call(ctx context.Context, request *embedding.Request) (*embedding.Response, error) {
	if e == nil || e.api == nil {
		return nil, errors.New("mistral: nil EmbeddingModel")
	}
	if err := request.Validate(); err != nil {
		return nil, err
	}
	effective := *request
	options, err := e.defaults.Resolve(request.Options)
	if err != nil {
		return nil, err
	}
	effective.Options = options
	native, _, err := options.Extensions.Decode[EmbeddingRequestOptions](EmbeddingRequestExtensionKey)
	if err != nil {
		return nil, err
	}
	response, err := e.api.embedding(ctx, &embeddingRequest{
		Model: options.Model, Input: request.Texts, OutputDimension: options.Dimensions,
		EncodingFormat: embeddingFloatEncoding, OutputDType: embeddingFloatEncoding, Metadata: native.Metadata,
	})
	if err != nil {
		return nil, err
	}
	mapped, err := response.project(len(request.Texts))
	if err != nil {
		return nil, err
	}
	if err := mapped.ValidateFor(&effective); err != nil {
		return nil, err
	}
	return mapped, nil
}

type embeddingRequest struct {
	Model           string       `json:"model"`
	Input           []string     `json:"input"`
	OutputDimension *int64       `json:"output_dimension,omitzero"`
	EncodingFormat  string       `json:"encoding_format"`
	OutputDType     string       `json:"output_dtype"`
	Metadata        metadata.Map `json:"metadata,omitzero"`
}

type embeddingResponse struct {
	ID    string `json:"id"`
	Model string `json:"model"`
	Data  []*struct {
		Index     *int       `json:"index"`
		Embedding []*float64 `json:"embedding"`
	} `json:"data"`
	Usage *struct {
		PromptTokens *int64 `json:"prompt_tokens"`
	} `json:"usage"`
}

func (e *embeddingResponse) project(expected int) (*embedding.Response, error) {
	if e == nil {
		return nil, fmt.Errorf("%w: mistral: missing embedding response", embedding.ErrInvalidResponse)
	}
	outputs := make([]*embedding.Output, expected)
	for position, result := range e.Data {
		if result == nil || result.Index == nil {
			return nil, fmt.Errorf("%w: mistral: embedding result %d requires an index", embedding.ErrInvalidResponse, position)
		}
		values := make([]float64, len(result.Embedding))
		for coordinate, value := range result.Embedding {
			if value == nil {
				return nil, fmt.Errorf("%w: mistral: embedding result %d coordinate %d is null", embedding.ErrInvalidResponse, position, coordinate)
			}
			values[coordinate] = *value
		}
		if err := embedding.PlaceOutput(outputs, *result.Index, values, nil); err != nil {
			return nil, fmt.Errorf("mistral: embedding result: %w", err)
		}
	}
	meta := &embedding.ResponseMetadata{Model: e.Model}
	if e.ID != "" {
		if err := meta.Extra.Set(embeddingResponseIDKey, e.ID); err != nil {
			return nil, err
		}
	}
	if e.Usage != nil && e.Usage.PromptTokens != nil {
		meta.Usage = &embedding.Usage{InputTokens: *e.Usage.PromptTokens}
	}
	return embedding.NewResponse(outputs, meta)
}
