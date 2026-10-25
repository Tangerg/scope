package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"net/http"

	"github.com/openai/openai-go/v3/responses"

	corechat "github.com/Tangerg/scope/core/chat"
)

// DefaultOptions are copied during construction; callers may select the model
// per request.
type ResponsesConfig struct {
	APIKey         string
	DefaultOptions corechat.Options
	BaseURL        string
	HTTPClient     *http.Client
	Headers        http.Header
}

func (r ResponsesConfig) Validate() error {
	if err := (apiConfig{APIKey: r.APIKey, BaseURL: r.BaseURL}).validate(); err != nil {
		return err
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
	dialect  ResponsesDialect
}

var (
	_ corechat.Model    = (*Responses)(nil)
	_ corechat.Streamer = (*Responses)(nil)
)

func NewResponses(ctx context.Context, config ResponsesConfig) (*Responses, error) {
	return NewCompatibleResponses(ctx, config, ResponsesDialect{Provider: "openai"})
}

// NewCompatibleResponses shares the Responses event lifecycle with a provider
// that explicitly implements it. Provider wrappers expose only supported
// optional capabilities such as model discovery or input-token counting.
func NewCompatibleResponses(ctx context.Context, config ResponsesConfig, dialect ResponsesDialect) (*Responses, error) {
	if err := dialect.Validate(); err != nil {
		return nil, err
	}
	if err := config.Validate(); err != nil {
		return nil, err
	}
	api, err := newAPI(ctx, apiConfig{APIKey: config.APIKey, BaseURL: config.BaseURL, HTTPClient: config.HTTPClient, Headers: config.Headers})
	if err != nil {
		return nil, err
	}
	return &Responses{api: api, defaults: config.DefaultOptions.Clone(), dialect: dialect}, nil
}

// ListModels returns the model IDs visible to this binding in provider order.
// Both limits must be positive. maxResponseBytes bounds the total response bytes
// across all pages; maxModels counts advertised entries, including duplicates.
// Failure or incomplete pagination returns no IDs. The caller owns the deadline;
// discovery performs no retries and preserves HTTPStatus and HTTPHeader errors.
func (r *Responses) ListModels(ctx context.Context, maxModels int, maxResponseBytes int64) ([]string, error) {
	return r.api.listModels(ctx, maxModels, maxResponseBytes)
}

func (r *Responses) Call(ctx context.Context, req *corechat.Request) (*corechat.Response, error) {
	var accumulator corechat.ResponseAccumulator
	for delta, err := range r.Stream(ctx, req) {
		if err != nil {
			return nil, err
		}
		if addErr := accumulator.Add(delta); addErr != nil {
			return nil, addErr
		}
	}
	return accumulator.Response()
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
// response deltas. Each function-call item owns its fixed call identity and
// open lifetime. Native item IDs bind when first reported; omitted IDs never
// replace a known identity. Reused identities, changed correlations, or
// arguments after item completion return [corechat.ErrInvalidResponse]. Incomplete responses
// may preserve a tool whose generation did not reach item completion. Completion
// is published after the native body ends successfully; later events are invalid.
func (r *Responses) Stream(ctx context.Context, req *corechat.Request) iter.Seq2[*corechat.ResponseDelta, error] {
	return func(yield func(*corechat.ResponseDelta, error) bool) {
		params, err := r.buildResponsesRequest(req)
		if err != nil {
			yield(nil, &corechat.CallFailure{Kind: corechat.CallFailureNotExecuted, Cause: err})
			return
		}
		stream, err := r.api.responseNewStream(ctx, params)
		if err != nil {
			yield(nil, err)
			return
		}
		defer stream.Close()

		state := newResponsesStreamState(r.dialect.Provider + "/responses_response")
		var terminal *corechat.ResponseDelta
		for stream.Next() {
			if terminal != nil {
				yield(nil, fmt.Errorf("%w: openai responses: event after terminal response", corechat.ErrInvalidResponse))
				return
			}
			response, mapErr := state.addEvent(stream.Current())
			if mapErr != nil {
				yield(nil, mapErr)
				return
			}
			if r.dialect.PreserveStreamEvents {
				if response == nil {
					response = &corechat.ResponseDelta{}
				}
				if response.Metadata == nil {
					response.Metadata = &corechat.ResponseMetadata{}
				}
				if err := response.Metadata.Extra.Set(r.dialect.Provider+"/responses_stream_event", json.RawMessage(stream.Current().RawJSON())); err != nil {
					yield(nil, err)
					return
				}
			}
			if response == nil {
				continue
			}
			if response.FinishReason != "" {
				terminal = response
				continue
			}
			if !yield(response, nil) {
				return
			}
		}
		if streamErr := stream.Err(); streamErr != nil {
			yield(nil, r.api.wrapError(streamErr))
			return
		}
		if terminal == nil {
			yield(nil, fmt.Errorf("%w: openai responses: stream ended without a terminal response", corechat.ErrInvalidResponse))
			return
		}
		yield(terminal, nil)
	}
}

func (r *Responses) buildResponsesRequest(req *corechat.Request) (*responses.ResponseNewParams, error) {
	if r == nil || r.api == nil {
		return nil, errors.New("openai responses: nil Responses")
	}
	projection, err := newResponsesRequestProjection(req, r.defaults, r.dialect)
	if err != nil {
		return nil, err
	}
	return projection.project()
}
