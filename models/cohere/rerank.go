package cohere

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	cohere "github.com/cohere-ai/cohere-go/v2"

	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/rerank"
)

// RerankRequestOptions contains Cohere controls that do not alter Core's
// reranking result semantics.
type RerankRequestOptions struct {
	MaxTokensPerDocument *int `json:"max_tokens_per_document,omitempty"`
	Priority             *int `json:"priority,omitempty"`
}

// RerankModelConfig binds provider access and defaults shared by every reranking call.
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

// RerankModel implements the Core reranking contract with Cohere.
type RerankModel struct {
	api            *api
	defaultOptions rerank.Options
}

// NewRerankModel rejects an invalid provider binding before the first reranking call.
func NewRerankModel(_ context.Context, config RerankModelConfig) (*RerankModel, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	api, err := newAPI(apiConfig{APIKey: config.APIKey, BaseURL: config.BaseURL, HTTPClient: config.HTTPClient})
	if err != nil {
		return nil, err
	}
	return &RerankModel{api: api, defaultOptions: config.DefaultOptions.Clone()}, nil
}

func (r *RerankModel) buildAPIRequest(request *rerank.Request) (*cohere.V2RerankRequest, error) {
	effective, err := r.defaultOptions.Resolve(request.Options)
	if err != nil {
		return nil, err
	}
	extension, _, err := effective.Extensions.Decode[RerankRequestOptions](RerankRequestExtensionKey)
	if err != nil {
		return nil, fmt.Errorf("cohere: decode rerank extension: %w", err)
	}
	value := &cohere.V2RerankRequest{
		Model:           effective.Model,
		Query:           request.Query,
		Documents:       request.Documents,
		MaxTokensPerDoc: extension.MaxTokensPerDocument,
		Priority:        extension.Priority,
	}
	if effective.TopK == 0 {
		value.TopN = nil
	} else {
		value.TopN = new(effective.TopK)
	}
	return value, nil
}

func (r *RerankModel) buildResponse(apiResponse *cohere.V2RerankResponse) (*rerank.Response, error) {
	if apiResponse == nil {
		return nil, errors.New("cohere: rerank response is nil")
	}
	results := make([]*rerank.Result, len(apiResponse.Results))
	for position, item := range apiResponse.Results {
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
	var extra metadata.Map
	if apiResponse.Id != nil {
		if err := extra.Set(RerankRequestIDMetadataKey, *apiResponse.Id); err != nil {
			return nil, err
		}
	}
	if apiResponse.Meta != nil {
		if apiResponse.Meta.Tokens != nil && apiResponse.Meta.Tokens.InputTokens != nil {
			responseMetadata.Usage = &rerank.Usage{InputTokens: int64(*apiResponse.Meta.Tokens.InputTokens)}
		}
		if apiResponse.Meta.BilledUnits != nil && apiResponse.Meta.BilledUnits.SearchUnits != nil {
			if err := extra.Set(RerankSearchUnitsMetadataKey, *apiResponse.Meta.BilledUnits.SearchUnits); err != nil {
				return nil, err
			}
		}
	}
	responseMetadata.Extra = extra
	return rerank.NewResponse(results, responseMetadata)
}

func (r *RerankModel) Call(ctx context.Context, request *rerank.Request) (*rerank.Response, error) {
	if err := request.Validate(); err != nil {
		return nil, err
	}
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
