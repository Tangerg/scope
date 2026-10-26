package cohere

import (
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"net/http"

	cohere "github.com/cohere-ai/cohere-go/v2"

	"github.com/Tangerg/scope/core/rerank"
)

// RerankRequestOptions contains Cohere controls that do not alter Core's
// reranking result semantics. Each field is keyed by the name Cohere's own
// reference uses, so a caller reading those docs sets the key they read.
type RerankRequestOptions struct {
	MaxTokensPerDoc *int `json:"max_tokens_per_doc,omitzero"`
	Priority        *int `json:"priority,omitzero"`
}

func (r *RerankRequestOptions) UnmarshalJSON(data []byte) error {
	type wireOptions RerankRequestOptions
	var decoded *wireOptions
	if err := jsonv2.Unmarshal(data, &decoded, jsonv2.RejectUnknownMembers(true)); err != nil {
		return fmt.Errorf("cohere: rerank options: %w", err)
	}
	if decoded == nil {
		return errors.New("cohere: rerank options must be an object")
	}
	*r = RerankRequestOptions(*decoded)
	return nil
}

type RerankModelConfig struct {
	APIKey         string
	DefaultOptions rerank.Options
	BaseURL        string
	HTTPClient     *http.Client
}

func (r RerankModelConfig) Validate() error {
	if r.APIKey == "" {
		return errors.New("cohere: APIKey is required")
	}
	if r.DefaultOptions.Model == "" {
		return errors.New("cohere: DefaultOptions.Model is required")
	}
	return r.DefaultOptions.Validate()
}

var _ rerank.Model = (*RerankModel)(nil)

type RerankModel struct {
	api            *api
	defaultOptions rerank.Options
}

func NewRerankModel(ctx context.Context, config RerankModelConfig) (*RerankModel, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	api, err := newAPI(ctx, apiConfig{APIKey: config.APIKey, BaseURL: config.BaseURL, HTTPClient: config.HTTPClient})
	if err != nil {
		return nil, err
	}
	return &RerankModel{api: api, defaultOptions: config.DefaultOptions.Clone()}, nil
}

func (r *RerankModel) buildAPIRequest(request *rerank.Request) (*cohere.V2RerankRequest, error) {
	effective := request.Options
	extension, _, err := effective.Extensions.Decode[RerankRequestOptions](RerankRequestExtensionKey)
	if err != nil {
		return nil, fmt.Errorf("cohere: decode rerank extension: %w", err)
	}
	value := &cohere.V2RerankRequest{
		Model:           effective.Model,
		Query:           request.Query,
		Documents:       request.Documents,
		MaxTokensPerDoc: extension.MaxTokensPerDoc,
		Priority:        extension.Priority,
	}
	if effective.TopK != nil && *effective.TopK > 0 {
		value.TopN = effective.TopK
	}
	return value, nil
}

func (r *RerankModel) buildResponse(apiResponse *nativeResponse[*cohere.V2RerankResponse]) (*rerank.Response, error) {
	if apiResponse == nil || apiResponse.value == nil {
		return nil, errors.New("cohere: rerank response is nil")
	}
	results := make([]*rerank.Result, len(apiResponse.value.Results))
	for position, item := range apiResponse.value.Results {
		if item == nil {
			return nil, fmt.Errorf("cohere: rerank response result %d is nil", position)
		}
		result, err := rerank.NewResult(item.Index, rerank.Score(item.RelevanceScore))
		if err != nil {
			return nil, err
		}
		results[position] = result
	}

	responseMetadata := &rerank.ResponseMetadata{}
	extra, err := apiResponse.usage.metadata()
	if err != nil {
		return nil, err
	}
	if apiResponse.value.Id != nil {
		if err := extra.Set(RerankRequestIDMetadataKey, *apiResponse.value.Id); err != nil {
			return nil, err
		}
	}
	if apiResponse.usage.Tokens.InputTokens != nil {
		responseMetadata.Usage = &rerank.Usage{InputTokens: int64(*apiResponse.usage.Tokens.InputTokens)}
	}

	responseMetadata.Extra = extra
	return rerank.NewResponse(results, responseMetadata)
}

func (r *RerankModel) Call(ctx context.Context, request *rerank.Request) (*rerank.Response, error) {
	if err := request.Validate(); err != nil {
		return nil, err
	}
	effectiveRequest := *request
	var err error
	effectiveRequest.Options, err = r.defaultOptions.Resolve(request.Options)
	if err != nil {
		return nil, err
	}
	if err = effectiveRequest.Validate(); err != nil {
		return nil, err
	}
	request = &effectiveRequest
	apiRequest, err := r.buildAPIRequest(request)
	if err != nil {
		return nil, err
	}
	apiResponse, err := r.api.rerank(ctx, apiRequest)
	if err != nil {
		return nil, err
	}
	response, err := r.buildResponse(apiResponse)
	if err != nil {
		return nil, err
	}
	if err := response.ValidateFor(request); err != nil {
		return nil, err
	}
	return response, nil
}
