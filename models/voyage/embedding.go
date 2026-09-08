package voyage

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/Tangerg/scope/core/embedding"
)

// EmbeddingModelConfig binds provider access and defaults shared by every embedding call.
type EmbeddingModelConfig struct {
	APIKey         string
	DefaultOptions embedding.Options

	// BaseURL / HTTPClient mirror [APIConfig] for callers that need to
	// proxy through a custom endpoint or share an http.Client.
	BaseURL    string
	HTTPClient *http.Client
}

func (e EmbeddingModelConfig) Validate() error {
	if e.APIKey == "" {
		return errors.New("voyage: APIKey is required")
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
// Voyage-specific knobs that don't fit the generic surface — InputType
// ("query" / "document" for asymmetric retrieval), Truncation,
// OutputDtype (int8/uint8/binary quantization) — are reached via the
// extension-threaded SDK params, see [getOptionsParams] and the
// [EmbeddingRequest] struct.
type EmbeddingModel struct {
	api            *api
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
		defaultOptions: config.DefaultOptions.Clone(),
	}, nil
}

func (e *EmbeddingModel) buildAPIRequest(req *embedding.Request) (*embeddingRequest, error) {
	effectiveOptions, err := e.defaultOptions.Resolve(req.Options)
	if err != nil {
		return nil, err
	}

	apiReqValue, _, err := effectiveOptions.Extensions.Decode[embeddingRequest](EmbeddingRequestExtensionKey)

	apiReq := &apiReqValue
	if err != nil {
		return nil, err
	}

	apiReq.Model = effectiveOptions.Model
	apiReq.Input = req.Texts

	if effectiveOptions.Dimensions != nil {
		apiReq.OutputDimension = effectiveOptions.Dimensions
	}
	if apiReq.OutputDtype == "" {
		apiReq.OutputDtype = "float"
	}
	if apiReq.OutputDtype != "float" {
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
func (e *EmbeddingModel) buildResponse(apiResp *embeddingResponse, expectedResults int) (*embedding.Response, error) {
	outputs := make([]*embedding.Output, expectedResults)
	for _, item := range apiResp.Data {
		if err := embedding.PlaceOutput(outputs, int(item.Index), item.Embedding, nil); err != nil {
			return nil, fmt.Errorf("voyage: embedding response: %w", err)
		}
	}

	meta := &embedding.ResponseMetadata{
		Model: apiResp.Model,
		Usage: &embedding.Usage{
			InputTokens: apiResp.Usage.TotalTokens,
		},
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
