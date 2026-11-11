package voyage

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/invocation"
	"github.com/Tangerg/scope/core/metadata"
)

// MaxTextsPerEmbedRequest is the documented ceiling for one embed call: "the
// maximum length of the list is 1,000". A larger request is refused here rather
// than sent to be rejected. Voyage also caps the total token count per request,
// by model rather than uniformly, which a text count cannot predict; that limit
// surfaces as a provider error.
const MaxTextsPerEmbedRequest = 1000

const (
	embeddingFieldModel           = "model"
	embeddingFieldInput           = "input"
	embeddingFieldOutputDimension = "output_dimension"
	embeddingDtypeFloat           = "float"
)

type EmbeddingModelConfig struct {
	APIKey         string
	DefaultOptions embedding.Options

	// BaseURL and HTTPClient support callers that need to
	// proxy through a custom endpoint or share an http.Client.
	BaseURL    string
	HTTPClient *http.Client
}

func (e EmbeddingModelConfig) apiConfig() apiConfig {
	return apiConfig{
		APIKey:     e.APIKey,
		BaseURL:    e.BaseURL,
		HTTPClient: e.HTTPClient,
	}
}

func (e EmbeddingModelConfig) Validate() error {
	if err := e.apiConfig().validate(); err != nil {
		return err
	}
	if e.DefaultOptions.Model == "" {
		return errors.New("voyage: DefaultOptions.Model is required")
	}
	if err := e.DefaultOptions.Validate(); err != nil {
		return err
	}
	return nil
}

var _ embedding.Model = (*EmbeddingModel)(nil)

// EmbeddingModel wraps Voyage AI's /embeddings endpoint. Voyage is
// Anthropic's officially recommended embedding provider, so this gives
// Anthropic-centric stacks a first-class RAG embedder without routing
// through OpenAI/Google.
//
// Current general-purpose models are voyage-4-large, voyage-4, and
// voyage-4-lite. Specialized models such as voyage-code-3 remain supported.
//
// Provider-specific input_type (query or document) and truncation use
// EmbeddingRequestExtensionKey. Core owns model, input, and dimensions.
// Only float output can be represented by the Core embedding protocol.
type EmbeddingModel struct {
	api            *api
	defaultOptions embedding.Options
}

func NewEmbeddingModel(ctx context.Context, config EmbeddingModelConfig) (*EmbeddingModel, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}

	api, err := newAPI(ctx, config.apiConfig())
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
	for _, field := range []string{embeddingFieldModel, embeddingFieldInput, embeddingFieldOutputDimension} {
		if _, exists := nativeFields[field]; exists {
			return nil, fmt.Errorf("voyage: extension %q field %q: %w", EmbeddingRequestExtensionKey, field, metadata.ErrCoreOwnedField)
		}
	}

	apiReqValue, _, err := effectiveOptions.Extensions.Decode[embeddingRequest](EmbeddingRequestExtensionKey)

	apiReq := &apiReqValue
	if err != nil {
		return nil, err
	}

	apiReq.Model = effectiveOptions.Model
	if apiReq.InputType != nil {
		if err := apiReq.InputType.validate(); err != nil {
			return nil, err
		}
	}
	if len(req.Texts) > MaxTextsPerEmbedRequest {
		return nil, fmt.Errorf("voyage: embed accepts at most %d texts per call, got %d",
			MaxTextsPerEmbedRequest, len(req.Texts))
	}

	apiReq.Input = req.Texts

	if effectiveOptions.Dimensions != nil {
		apiReq.OutputDimension = effectiveOptions.Dimensions
	}
	if apiReq.OutputDtype == "" {
		apiReq.OutputDtype = embeddingDtypeFloat
	}
	if apiReq.OutputDtype != embeddingDtypeFloat {
		return nil, fmt.Errorf("voyage: extension %q output_dtype %q cannot be represented by Core float embeddings", EmbeddingRequestExtensionKey, apiReq.OutputDtype)
	}
	if apiReq.EncodingFormat != "" {
		return nil, fmt.Errorf("voyage: extension %q encoding_format is unsupported by Core float embeddings", EmbeddingRequestExtensionKey)
	}

	return apiReq, nil
}

// buildResponse assembles one output per input text. Sizing the slice from the
// request rather than the reply is what makes every mismatch a local error: an
// index the request cannot hold and a repeated index are refused as they
// arrive, and an input the provider never answered stays nil and fails when the
// Response is built, naming the text that went unanswered.
func (e *EmbeddingModel) buildResponse(apiResp *embeddingResponse, req *embedding.Request) (*embedding.Response, error) {
	outputs := make([]*embedding.Output, len(req.Texts))
	for position, item := range apiResp.Data {
		if item.Index == nil {
			return nil, fmt.Errorf("%w: voyage: embedding result %d requires index", embedding.ErrInvalidResponse, position)
		}
		if *item.Index < 0 || *item.Index >= int64(len(outputs)) {
			return nil, fmt.Errorf("%w: voyage: embedding index %d is outside the input batch", embedding.ErrInvalidResponse, *item.Index)
		}
		if err := embedding.PlaceOutput(outputs, int(*item.Index), item.Embedding, nil); err != nil {
			return nil, fmt.Errorf("voyage: embedding response: %w", err)
		}
	}

	meta := &embedding.ResponseMetadata{Model: apiResp.Model}
	if apiResp.Usage.TotalTokens != nil {
		meta.Usage = &embedding.Usage{InputTokens: *apiResp.Usage.TotalTokens}
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

	apiReq, err := e.buildAPIRequest(req)
	if err != nil {
		return nil, &invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: err}
	}

	apiResp, err := e.api.embedding(ctx, apiReq)
	if err != nil {
		return nil, &invocation.Failure{Kind: invocation.FailureUnknown, Cause: err}
	}

	return e.buildResponse(apiResp, req)
}
