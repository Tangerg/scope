package openai

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/openai/openai-go/v3"

	"github.com/Tangerg/scope/core/embedding"
)

// EmbeddingModelConfig binds provider access and defaults shared by every embedding call.
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
	if e.APIKey == "" {
		return errors.New("openai: APIKey is required")
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

// NewEmbeddingModel rejects an invalid provider binding before the first embedding call.
func NewEmbeddingModel(config EmbeddingModelConfig) (*EmbeddingModel, error) {
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
	effectiveOptions, err := e.defaultOptions.Resolve(req.Options)
	if err != nil {
		return nil, err
	}

	fields, err := decodeRequestFields(effectiveOptions.Extensions, protocolModalityRequestExtensionKey(e.provider, "embedding"), "model", "input", "dimensions")
	if err != nil {
		return nil, err
	}
	params := &openai.EmbeddingNewParams{}
	params.SetExtraFields(fields)

	params.Model = effectiveOptions.Model
	params.Input = openai.EmbeddingNewParamsInputUnion{
		OfArrayOfStrings: req.Texts,
	}

	if effectiveOptions.Dimensions != nil {
		params.Dimensions = openai.Int(*effectiveOptions.Dimensions)
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
		Usage: &embedding.Usage{
			InputTokens: apiResp.Usage.PromptTokens,
		},
	}

	outputs := make([]*embedding.Output, expectedResults)
	for _, item := range apiResp.Data {
		if err := embedding.PlaceOutput(outputs, int(item.Index), item.Embedding, nil); err != nil {
			return nil, fmt.Errorf("openai: embeddings response: %w", err)
		}
	}

	return embedding.NewResponse(outputs, meta)
}

func (e *EmbeddingModel) Call(ctx context.Context, req *embedding.Request) (response *embedding.Response, err error) {
	if err = req.Validate(); err != nil {
		return nil, err
	}
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
