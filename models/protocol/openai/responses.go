package openai

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"net/http"

	corechat "github.com/Tangerg/scope/core/chat"
)

// ResponsesConfig configures an OpenAI Responses adapter. DefaultOptions are
// copied during construction; callers may select the model per request.
type ResponsesConfig struct {
	APIKey         string
	DefaultOptions corechat.Options
	BaseURL        string
	HTTPClient     *http.Client
	Headers        http.Header
}

func (r ResponsesConfig) Validate() error {
	if r.APIKey == "" {
		return errors.New("openai responses: API key is required")
	}
	if err := r.DefaultOptions.Validate(); err != nil {
		return fmt.Errorf("openai responses: default options: %w", err)
	}
	return nil
}

const (
	// ResponsesRequestExtensionKey stores official Responses API parameters in
	// a Core request for fields without a provider-neutral equivalent.
	ResponsesRequestExtensionKey = "openai/responses_request"
	// ResponsesResponseExtensionKey preserves the complete official Responses
	// API response, including output item types Core does not normalize.
	ResponsesResponseExtensionKey = "openai/responses_response"
	responsesItemTypeMessage      = "message"
	responsesItemTypeReasoning    = "reasoning"
	responsesItemTypeFunctionCall = "function_call"
	responsesContentTypeText      = "output_text"
	responsesContentTypeRefusal   = "refusal"
	responsesIncompleteMaxTokens  = "max_output_tokens"
	responsesIncompleteFiltered   = "content_filter"
)

// Responses adapts OpenAI's ordered Responses API output to the minimal
// Core chat Model and Streamer capabilities.
type Responses struct {
	api      *api
	defaults corechat.Options
}

var (
	_ corechat.Model    = (*Responses)(nil)
	_ corechat.Streamer = (*Responses)(nil)
)

// NewResponses rejects an invalid provider binding before the first Responses call.
func NewResponses(_ context.Context, config ResponsesConfig) (*Responses, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	api, err := newAPI(apiConfig{APIKey: config.APIKey, BaseURL: config.BaseURL, HTTPClient: config.HTTPClient, Headers: config.Headers})
	if err != nil {
		return nil, err
	}
	return &Responses{api: api, defaults: config.DefaultOptions.Clone()}, nil
}

func (r *Responses) Call(ctx context.Context, req *corechat.Request) (*corechat.Response, error) {
	params, err := r.buildResponsesRequest(req)
	if err != nil {
		return nil, err
	}
	response, err := r.api.responseNew(ctx, params)
	if err != nil {
		return nil, err
	}
	return mapResponsesResponse(response)
}

// CountInputTokens calls the provider's Responses input-token endpoint with
// the same provider request projection used by Call.
func (r *Responses) CountInputTokens(ctx context.Context, req *corechat.Request) (int64, error) {
	params, err := r.buildResponsesRequest(req)
	if err != nil {
		return 0, err
	}
	countParams, err := projectResponsesInputTokenCount(params)
	if err != nil {
		return 0, err
	}
	response, err := r.api.responseInputTokensCount(ctx, countParams)
	if err != nil {
		return 0, err
	}
	return response.InputTokens, nil
}

// Stream performs one streaming Responses API request and yields ordered Core
// response deltas.
func (r *Responses) Stream(ctx context.Context, req *corechat.Request) iter.Seq2[*corechat.ResponseDelta, error] {
	return func(yield func(*corechat.ResponseDelta, error) bool) {
		params, err := r.buildResponsesRequest(req)
		if err != nil {
			yield(nil, err)
			return
		}
		stream, err := r.api.responseNewStream(ctx, params)
		if err != nil {
			yield(nil, err)
			return
		}
		defer stream.Close()

		state := newResponsesStreamState()
		for stream.Next() {
			response, include, mapErr := state.addEvent(stream.Current())
			if mapErr != nil {
				yield(nil, mapErr)
				return
			}
			if include && (!yield(response, nil) || response.FinishReason != "") {
				return
			}
		}
		if streamErr := stream.Err(); streamErr != nil {
			yield(nil, r.api.wrapError(streamErr))
			return
		}
		yield(nil, fmt.Errorf("%w: openai responses: stream ended without a terminal response", corechat.ErrInvalidResponse))
	}
}
