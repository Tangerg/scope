package cohere

import (
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"net/http"

	cohere "github.com/cohere-ai/cohere-go/v2"

	"github.com/Tangerg/scope/core/embedding"
)

// MaxTextsPerEmbedRequest is the documented ceiling for one embed call:
// "Maximum number of texts per call is 96". A larger request is refused here
// rather than sent to be rejected, and it cannot be split without turning one
// caller-visible call into several with their own partial-failure and usage
// accounting.
const MaxTextsPerEmbedRequest = 96

// EmbeddingRequestOptions supplies Cohere controls under
// EmbeddingRequestExtensionKey. InputType is required. EmbeddingTypes may be
// omitted or contain only float, which is the Core result representation.
// Core owns the input texts, model, and output dimensions.
type EmbeddingRequestOptions struct {
	InputType      string   `json:"input_type"`
	MaxTokens      *int     `json:"max_tokens,omitzero"`
	EmbeddingTypes []string `json:"embedding_types,omitzero"`
	Truncate       *string  `json:"truncate,omitzero"`
	Priority       *int     `json:"priority,omitzero"`
}

func (e *EmbeddingRequestOptions) UnmarshalJSON(data []byte) error {
	type wireOptions EmbeddingRequestOptions
	var decoded *wireOptions
	if err := jsonv2.Unmarshal(data, &decoded, jsonv2.RejectUnknownMembers(true)); err != nil {
		return fmt.Errorf("cohere: embedding options: %w", err)
	}
	if decoded == nil {
		return errors.New("cohere: embedding options must be an object")
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
		return errors.New("cohere: APIKey is required")
	}
	if e.DefaultOptions.Model == "" {
		return errors.New("cohere: DefaultOptions.Model is required")
	}
	if err := e.DefaultOptions.Validate(); err != nil {
		return err
	}
	return nil
}

var _ embedding.Model = (*EmbeddingModel)(nil)

// EmbeddingModel wraps Cohere's v2 embed endpoint.
//
// Supported models: embed-english-v3.0, embed-multilingual-v3.0,
// embed-english-light-v3.0, embed-multilingual-light-v3.0, embed-v4.0.
// v4 is the only family that supports OutputDimension; older v3 models
// have a fixed 1024-dim output.
type EmbeddingModel struct {
	api            *api
	defaultOptions embedding.Options
}

func NewEmbeddingModel(ctx context.Context, config EmbeddingModelConfig) (*EmbeddingModel, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}

	api, err := newAPI(ctx, apiConfig{APIKey: config.APIKey, BaseURL: config.BaseURL, HTTPClient: config.HTTPClient})
	if err != nil {
		return nil, err
	}

	return &EmbeddingModel{
		api:            api,
		defaultOptions: config.DefaultOptions.Clone(),
	}, nil
}

func (e *EmbeddingModel) buildAPIRequest(req *embedding.Request) (*cohere.V2EmbedRequest, error) {
	effectiveOptions := req.Options

	options, _, err := effectiveOptions.Extensions.Decode[EmbeddingRequestOptions](EmbeddingRequestExtensionKey)
	if err != nil {
		return nil, err
	}
	apiReq := &cohere.V2EmbedRequest{
		InputType: cohere.EmbedInputType(options.InputType),
		MaxTokens: options.MaxTokens,
		Priority:  options.Priority,
	}
	for _, encoding := range options.EmbeddingTypes {
		apiReq.EmbeddingTypes = append(apiReq.EmbeddingTypes, cohere.EmbeddingType(encoding))
	}
	if options.Truncate != nil {
		apiReq.Truncate = new(cohere.V2EmbedRequestTruncate(*options.Truncate))
	}

	if len(req.Texts) > MaxTextsPerEmbedRequest {
		return nil, fmt.Errorf("cohere: embed accepts at most %d texts per call, got %d",
			MaxTextsPerEmbedRequest, len(req.Texts))
	}

	apiReq.Model = effectiveOptions.Model
	apiReq.Texts = req.Texts

	if apiReq.InputType == "" {
		return nil, fmt.Errorf("cohere: extension %q input_type is required; choose search_document, search_query, classification, or clustering", EmbeddingRequestExtensionKey)
	}

	// Cohere requires at least one embedding type. Core normalizes provider
	// responses to float vectors, so request that wire shape explicitly.
	if len(apiReq.EmbeddingTypes) == 0 {
		apiReq.EmbeddingTypes = []cohere.EmbeddingType{cohere.EmbeddingTypeFloat}
	}

	if len(apiReq.EmbeddingTypes) != 1 || apiReq.EmbeddingTypes[0] != cohere.EmbeddingTypeFloat {
		return nil, fmt.Errorf("cohere: extension %q embedding_types must contain only float", EmbeddingRequestExtensionKey)
	}

	if effectiveOptions.Dimensions != nil {
		value := int(*effectiveOptions.Dimensions)
		if int64(value) != *effectiveOptions.Dimensions {
			return nil, fmt.Errorf("cohere: embedding: dimensions: %d exceeds int", *effectiveOptions.Dimensions)
		}
		apiReq.OutputDimension = &value
	}

	return apiReq, nil
}

// buildResponse assembles one output per input text. This provider answers in
// input order without tagging each embedding, so arrival order is the position,
// and sizing the slice from the request is what turns every mismatch into a
// local error: an extra embedding has nowhere to go, and an input the provider
// never answered stays nil and fails when the Response is built, naming the
// text that went unanswered.
func (e *EmbeddingModel) buildResponse(apiResp *nativeResponse[*cohere.EmbedByTypeResponse], expectedResults int) (*embedding.Response, error) {
	if apiResp.value.Embeddings == nil {
		return nil, errors.New("cohere: embed response has no float embeddings")
	}

	outputs := make([]*embedding.Output, expectedResults)
	for index, vec := range apiResp.value.Embeddings.Float {
		if err := embedding.PlaceOutput(outputs, index, vec, nil); err != nil {
			return nil, fmt.Errorf("cohere: embed response: %w", err)
		}
	}

	meta := &embedding.ResponseMetadata{}
	if apiResp.usage.Tokens.InputTokens != nil {
		meta.Usage = &embedding.Usage{InputTokens: int64(*apiResp.usage.Tokens.InputTokens)}
	}
	var err error
	meta.Extra, err = apiResp.usage.metadata()
	if err != nil {
		return nil, err
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

	apiResp, err := e.api.embed(ctx, apiReq)
	if err != nil {
		return nil, err
	}

	return e.buildResponse(apiResp, len(req.Texts))
}
