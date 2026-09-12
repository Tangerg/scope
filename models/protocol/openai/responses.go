package openai

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"net/http"
	"slices"

	openaisdk "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"

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

func (r *Responses) buildResponsesRequest(req *corechat.Request) (*responses.ResponseNewParams, error) {
	if r == nil || r.api == nil {
		return nil, errors.New("openai responses: nil Responses")
	}
	if err := req.Validate(); err != nil {
		return nil, fmt.Errorf("openai responses: request: %w", err)
	}
	if err := rejectCoreOwnedResponsesExtension(req.Options.Extensions); err != nil {
		return nil, err
	}
	params, found, err := req.Options.Extensions.Decode[responses.ResponseNewParams](ResponsesRequestExtensionKey)
	if err != nil {
		return nil, fmt.Errorf("openai responses: extension %q: %w", ResponsesRequestExtensionKey, err)
	}
	if !found {
		params = responses.ResponseNewParams{}
	}

	options, err := r.defaults.Resolve(req.Options)
	if err != nil {
		return nil, fmt.Errorf("openai responses: options: %w", err)
	}
	if options.Model == "" {
		return nil, errors.New("openai responses: model is required in defaults or request options")
	}
	if options.FrequencyPenalty != nil || options.PresencePenalty != nil || options.TopK != nil || len(options.Stop) != 0 {
		return nil, errors.New("openai responses: frequency_penalty, presence_penalty, top_k, and stop are not supported")
	}
	params.Model = shared.ResponsesModel(options.Model)
	if options.MaxOutputTokens != nil {
		params.MaxOutputTokens = openaisdk.Int(*options.MaxOutputTokens)
	}
	if options.Temperature != nil {
		params.Temperature = openaisdk.Float(*options.Temperature)
	}
	if options.TopP != nil {
		params.TopP = openaisdk.Float(*options.TopP)
	}
	if req.ToolChoice != nil {
		switch req.ToolChoice.Mode {
		case corechat.ToolChoiceAuto, corechat.ToolChoiceNone, corechat.ToolChoiceRequired:
			params.ToolChoice.OfToolChoiceMode = openaisdk.Opt(responses.ToolChoiceOptions(req.ToolChoice.Mode))
		case corechat.ToolChoiceNamed:
			params.ToolChoice.OfFunctionTool = &responses.ToolChoiceFunctionParam{Name: req.ToolChoice.Name}
		}
		switch req.ToolChoice.Parallelism {
		case corechat.ToolParallelismAllow:
			params.ParallelToolCalls = openaisdk.Bool(true)
		case corechat.ToolParallelismSingle:
			params.ParallelToolCalls = openaisdk.Bool(false)
		}
	}
	reasoningEffort, err := mapReasoningEffort(options.ReasoningEffort)
	if err != nil {
		return nil, err
	}
	params.Reasoning.Effort = reasoningEffort
	if options.OutputFormat != nil {
		format, mapResponsesOutputFormatErr := mapResponsesOutputFormat(options.OutputFormat)
		if mapResponsesOutputFormatErr != nil {
			return nil, mapResponsesOutputFormatErr
		}
		params.Text.Format = format
	}
	if !slices.Contains(params.Include, responses.ResponseIncludableReasoningEncryptedContent) {
		params.Include = append(params.Include, responses.ResponseIncludableReasoningEncryptedContent)
	}

	items, err := mapResponsesInput(req.Messages)
	if err != nil {
		return nil, err
	}
	params.Input.OfInputItemList = items
	params.Tools, err = mapResponsesTools(req.Tools)
	if err != nil {
		return nil, err
	}
	return &params, nil
}
