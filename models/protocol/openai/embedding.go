package openai

import (
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"net/http"

	"github.com/openai/openai-go/v3"

	"github.com/Tangerg/scope/core/embedding"
)

type EmbeddingModelConfig struct {
	Provider       string
	APIKey         string
	DefaultOptions embedding.Options
	BaseURL        string
	HTTPClient     *http.Client
}

func (e EmbeddingModelConfig) Validate() error {
	if err := validateProvider(e.Provider); err != nil {
		return fmt.Errorf("openai: Provider: %w", err)
	}
	if err := (apiConfig{APIKey: e.APIKey, BaseURL: e.BaseURL}).validate(); err != nil {
		return err
	}
	if e.DefaultOptions.Model == "" {
		return errors.New("openai: DefaultOptions.Model is required")
	}
	if err := e.DefaultOptions.Validate(); err != nil {
		return err
	}
	return nil
}

var _ embedding.Model = (*EmbeddingModel)(nil)

// EmbeddingModel implements the OpenAI-compatible embedding protocol.
type EmbeddingModel struct {
	api            *api
	provider       string
	defaultOptions embedding.Options
}

func NewEmbeddingModel(_ context.Context, config EmbeddingModelConfig) (*EmbeddingModel, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}

	api, err := newAPI(apiConfig{
		APIKey:     config.APIKey,
		BaseURL:    config.BaseURL,
		HTTPClient: config.HTTPClient,
	})
	if err != nil {
		return nil, err
	}

	return &EmbeddingModel{
		api:            api,
		provider:       config.Provider,
		defaultOptions: config.DefaultOptions.Clone(),
	}, nil
}

func (e *EmbeddingModel) buildAPIEmbeddingRequest(req *embedding.Request) (*openai.EmbeddingNewParams, error) {
	fields, err := decodeRequestFields(req.Options.Extensions, protocolModalityRequestExtensionKey(e.provider, "embedding"), "model", "input", "dimensions")
	if err != nil {
		return nil, err
	}
	params := &openai.EmbeddingNewParams{}
	params.SetExtraFields(fields)

	params.Model = req.Options.Model
	params.Input = openai.EmbeddingNewParamsInputUnion{
		OfArrayOfStrings: req.Texts,
	}

	if req.Options.Dimensions != nil {
		params.Dimensions = openai.Int(*req.Options.Dimensions)
	}

	return params, nil
}

// buildEmbeddingResponse assembles one output per input text. Sizing the slice
// from the request rather than the reply is what makes every mismatch a local
// error: an index the request cannot hold and a repeated index are refused as
// they arrive, and an input the provider never answered stays nil and fails
// when the Response is built, naming the text that went unanswered.
func (e *EmbeddingModel) buildEmbeddingResponse(apiResp *openai.CreateEmbeddingResponse, expectedResults int) (*embedding.Response, error) {
	meta := &embedding.ResponseMetadata{
		Model: apiResp.Model,
	}
	if apiResp.Usage.JSON.PromptTokens.Valid() {
		meta.Usage = &embedding.Usage{InputTokens: apiResp.Usage.PromptTokens}
	}

	outputs := make([]*embedding.Output, expectedResults)
	for position, item := range apiResp.Data {
		if !item.JSON.Index.Valid() || !item.JSON.Embedding.Valid() {
			return nil, fmt.Errorf("%w: openai: embedding result %d requires index and embedding", embedding.ErrInvalidResponse, position)
		}
		var coordinates []*float64
		if err := jsonv2.Unmarshal([]byte(item.JSON.Embedding.Raw()), &coordinates); err != nil {
			return nil, fmt.Errorf("%w: openai: embedding result %d: %w", embedding.ErrInvalidResponse, position, err)
		}
		values := make([]float64, len(coordinates))
		for coordinate, value := range coordinates {
			if value == nil {
				return nil, fmt.Errorf("%w: openai: embedding result %d coordinate %d is null", embedding.ErrInvalidResponse, position, coordinate)
			}
			values[coordinate] = *value
		}
		if err := embedding.PlaceOutput(outputs, int(item.Index), values, nil); err != nil {
			return nil, fmt.Errorf("openai: embeddings response: %w", err)
		}
	}

	return embedding.NewResponse(outputs, meta)
}

func (e *EmbeddingModel) Call(ctx context.Context, req *embedding.Request) (response *embedding.Response, err error) {
	if err = req.Validate(); err != nil {
		return nil, err
	}
	options, err := e.defaultOptions.Resolve(req.Options)
	if err != nil {
		return nil, err
	}
	effectiveRequest := *req
	effectiveRequest.Options = options
	req = &effectiveRequest
	defer func() {
		if err == nil {
			err = response.ValidateFor(req)
		}
	}()

	apiReq, err := e.buildAPIEmbeddingRequest(req)
	if err != nil {
		return nil, err
	}

	apiResp, err := e.api.embedding(ctx, apiReq)
	if err != nil {
		return nil, err
	}

	return e.buildEmbeddingResponse(apiResp, len(req.Texts))
}
