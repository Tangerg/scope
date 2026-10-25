package jina

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/Tangerg/scope/core/embedding"
)

type EmbeddingModelConfig struct {
	APIKey         string
	DefaultOptions embedding.Options
	BaseURL        string
	HTTPClient     *http.Client
}

func (e EmbeddingModelConfig) Validate() error {
	if e.APIKey == "" {
		return errors.New("jina: APIKey is required")
	}
	if e.DefaultOptions.Model == "" {
		return errors.New("jina: DefaultOptions.Model is required")
	}
	if err := e.DefaultOptions.Validate(); err != nil {
		return err
	}
	return nil
}

var _ embedding.Model = (*EmbeddingModel)(nil)

// EmbeddingModel implements the Core embedding contract with Jina.
type EmbeddingModel struct {
	api            *api
	defaultOptions embedding.Options
}

func NewEmbeddingModel(ctx context.Context, config EmbeddingModelConfig) (*EmbeddingModel, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}

	api, err := newAPI(ctx, apiConfig{
		APIKey:     config.APIKey,
		BaseURL:    config.BaseURL,
		HTTPClient: config.HTTPClient,
	})
	if err != nil {
		return nil, err
	}

	return &EmbeddingModel{
		api:            api,
		defaultOptions: config.DefaultOptions.Clone(),
	}, nil
}

func (e *EmbeddingModel) buildAPIRequest(req *embedding.Request) (*embeddingRequest, error) {
	effectiveOptions := req.Options
	nativeFields, _, err := effectiveOptions.Extensions.Decode[map[string]any](EmbeddingRequestExtensionKey)
	if err != nil {
		return nil, err
	}
	for _, field := range []string{"model", "input", "dimensions"} {
		if _, exists := nativeFields[field]; exists {
			return nil, fmt.Errorf("jina: extension %q field %q is owned by Core", EmbeddingRequestExtensionKey, field)
		}
	}

	apiReqValue, _, err := effectiveOptions.Extensions.Decode[embeddingRequest](EmbeddingRequestExtensionKey)

	apiReq := &apiReqValue
	if err != nil {
		return nil, err
	}

	apiReq.Model = effectiveOptions.Model
	apiReq.Input = req.Texts

	if effectiveOptions.Dimensions != nil {
		apiReq.Dimensions = effectiveOptions.Dimensions
	}
	if apiReq.EmbeddingType == "" {
		apiReq.EmbeddingType = "float"
	}
	if apiReq.EmbeddingType != "float" {
		return nil, fmt.Errorf("jina: extension %q embedding_type %q cannot be represented by Core float embeddings", EmbeddingRequestExtensionKey, apiReq.EmbeddingType)
	}

	return apiReq, nil
}

// buildResponse assembles one output per input text. Sizing the slice from the
// request rather than the reply is what makes every mismatch a local error: an
// index the request cannot hold and a repeated index are refused as they
// arrive, and an input the provider never answered stays nil and fails when the
// Response is built, naming the text that went unanswered.
func (e *EmbeddingModel) buildResponse(apiResp *embeddingResponse, expectedResults int) (*embedding.Response, error) {
	outputs := make([]*embedding.Output, expectedResults)
	for position, item := range apiResp.Data {
		if item.Index == nil {
			return nil, fmt.Errorf("%w: jina: embedding result %d requires index", embedding.ErrInvalidResponse, position)
		}
		values := make([]float64, len(item.Embedding))
		for coordinate, value := range item.Embedding {
			if value == nil {
				return nil, fmt.Errorf("%w: jina: embedding result %d coordinate %d is null", embedding.ErrInvalidResponse, position, coordinate)
			}
			values[coordinate] = *value
		}
		if err := embedding.PlaceOutput(outputs, int(*item.Index), values, nil); err != nil {
			return nil, fmt.Errorf("jina: embedding response: %w", err)
		}
	}

	meta := &embedding.ResponseMetadata{Model: apiResp.Model}
	if apiResp.Usage.PromptTokens != nil {
		meta.Usage = &embedding.Usage{InputTokens: *apiResp.Usage.PromptTokens}
	}

	return embedding.NewResponse(outputs, meta)
}

func (e *EmbeddingModel) Call(ctx context.Context, req *embedding.Request) (response *embedding.Response, err error) {
	if err = req.Validate(); err != nil {
		return nil, err
	}
	effectiveRequest := *req
	effectiveRequest.Options, err = e.defaultOptions.Resolve(req.Options)
	if err != nil {
		return nil, err
	}
	req = &effectiveRequest
	defer func() {
		if err == nil {
			err = response.ValidateFor(req)
			if err != nil {
				response = nil
			}
		}
	}()

	apiReq, err := e.buildAPIRequest(req)
	if err != nil {
		return nil, err
	}

	apiResp, err := e.api.embedding(ctx, apiReq)
	if err != nil {
		return nil, err
	}

	return e.buildResponse(apiResp, len(req.Texts))
}
