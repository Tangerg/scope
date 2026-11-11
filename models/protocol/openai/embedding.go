package openai

import (
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"net/http"

	"github.com/openai/openai-go/v3"

	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/invocation"
	corevector "github.com/Tangerg/scope/core/vector"
)

type EmbeddingModelConfig struct {
	Provider       string
	APIKey         string
	DefaultOptions embedding.Options
	BaseURL        string
	HTTPClient     *http.Client
}

func (e EmbeddingModelConfig) Validate() error {
	if err := requireBindingEndpoint(e.Provider, e.BaseURL); err != nil {
		return err
	}
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

type EmbeddingModel struct {
	api            *api
	provider       string
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
		provider:       config.Provider,
		defaultOptions: config.DefaultOptions.Clone(),
	}, nil
}

// Embedding request fields Core owns or constrains.
const (
	embeddingFieldModel          = "model"
	embeddingFieldInput          = "input"
	embeddingFieldDimensions     = "dimensions"
	embeddingFieldEncodingFormat = "encoding_format"
)

func (e *EmbeddingModel) buildAPIEmbeddingRequest(req *embedding.Request) (*openai.EmbeddingNewParams, error) {
	fields, err := decodeRequestFields(req.Options.Extensions, e.provider+EmbeddingRequestExtensionSuffix, embeddingFieldModel, embeddingFieldInput, embeddingFieldDimensions)
	if err != nil {
		return nil, err
	}
	if format, set := fields[embeddingFieldEncodingFormat]; set && format != string(openai.EmbeddingNewParamsEncodingFormatFloat) {
		return nil, fmt.Errorf("%w: openai: embedding encoding_format must be float for a Core embedding", errors.ErrUnsupported)
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
func (e *EmbeddingModel) buildEmbeddingResponse(apiResp *openai.CreateEmbeddingResponse, req *embedding.Request) (*embedding.Response, error) {
	if apiResp == nil {
		return nil, fmt.Errorf("%w: openai: embedding response is null", embedding.ErrInvalidResponse)
	}
	meta := &embedding.ResponseMetadata{
		Model: apiResp.Model,
	}
	input, err := optionalCount(apiResp.Usage.JSON.PromptTokens)
	if err != nil {
		return nil, fmt.Errorf("%w: openai: input token usage: %w", embedding.ErrInvalidResponse, err)
	}
	if input != nil {
		meta.Usage = &embedding.Usage{InputTokens: *input}
	}

	outputs := make([]*embedding.Output, len(req.Texts))
	for position, item := range apiResp.Data {
		index, indexed := responseIndex(item.JSON.Index)
		if !indexed || !item.JSON.Embedding.Valid() {
			return nil, fmt.Errorf("%w: openai: embedding result %d requires index and embedding", embedding.ErrInvalidResponse, position)
		}
		var coordinates corevector.Coordinates[float64]
		if err := jsonv2.Unmarshal([]byte(item.JSON.Embedding.Raw()), &coordinates); err != nil {
			return nil, fmt.Errorf("%w: openai: embedding result %d: %w", embedding.ErrInvalidResponse, position, err)
		}
		if err := embedding.PlaceOutput(outputs, int(index), coordinates, nil); err != nil {
			return nil, fmt.Errorf("openai: embeddings response: %w", err)
		}
	}

	out := &embedding.Response{Outputs: outputs, Metadata: meta}
	if err := out.ValidateFor(req); err != nil {
		return nil, err
	}
	return out, nil
}

func (e *EmbeddingModel) Call(ctx context.Context, req *embedding.Request) (*embedding.Response, error) {
	if err := req.Validate(); err != nil {
		return nil, &invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: err}
	}
	options, err := e.defaultOptions.Resolve(req.Options)
	if err != nil {
		return nil, &invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: err}
	}
	effectiveRequest := *req
	effectiveRequest.Options = options
	req = &effectiveRequest

	apiReq, err := e.buildAPIEmbeddingRequest(req)
	if err != nil {
		return nil, &invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: err}
	}

	apiResp, err := e.api.embedding(ctx, apiReq)
	if err != nil {
		return nil, &invocation.Failure{Kind: invocation.FailureUnknown, Cause: err}
	}

	return e.buildEmbeddingResponse(apiResp, req)
}
