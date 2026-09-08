package jina

import (
	"context"
	"errors"
	"net/http"

	"github.com/Tangerg/scope/core/rerank"
)

// RerankModelConfig binds provider access and defaults shared by every reranking call.
type RerankModelConfig struct {
	APIKey         string
	DefaultOptions rerank.Options
	BaseURL        string
	HTTPClient     *http.Client
}

func (r RerankModelConfig) Validate() error {
	if r.APIKey == "" {
		return errors.New("jina: APIKey is required")
	}
	if r.DefaultOptions.Model == "" {
		return errors.New("jina: DefaultOptions.Model is required")
	}
	return r.DefaultOptions.Validate()
}

var _ rerank.Model = (*RerankModel)(nil)

// RerankModel implements the Core reranking contract with Jina.
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

func (r *RerankModel) buildAPIRequest(request *rerank.Request) (*rerankRequest, error) {
	effective, err := r.defaultOptions.Resolve(request.Options)
	if err != nil {
		return nil, err
	}
	apiRequest := &rerankRequest{
		Model: effective.Model, Query: request.Query, Documents: request.Documents,
	}
	if effective.TopK != 0 {
		apiRequest.TopN = new(effective.TopK)
	}
	return apiRequest, nil
}

func (r *RerankModel) buildResponse(apiResponse *rerankResponse) (*rerank.Response, error) {
	if apiResponse == nil {
		return nil, errors.New("jina: rerank response is nil")
	}
	results := make([]*rerank.Result, len(apiResponse.Results))
	for position, item := range apiResponse.Results {
		result, err := rerank.NewResult(item.Index, rerank.Score(item.RelevanceScore))
		if err != nil {
			return nil, err
		}
		results[position] = result
	}
	responseMetadata := &rerank.ResponseMetadata{Model: apiResponse.Model}
	if apiResponse.Usage.TotalTokens > 0 {
		responseMetadata.Usage = &rerank.Usage{InputTokens: apiResponse.Usage.TotalTokens}
	}
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
