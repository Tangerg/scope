package openai

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"maps"
	"net/http"
	"slices"

	openaisdk "github.com/openai/openai-go/v3"

	corechat "github.com/Tangerg/scope/core/chat"
)

const (
	// RequestExtensionKey identifies provider-owned Chat Completions fields
	// encoded as [RequestFields].
	RequestExtensionKey = "openai/request"
	// ResponseExtensionKey preserves the complete official Chat Completions
	// response after provider-neutral fields have been mapped.
	ResponseExtensionKey = "openai/response"
	// StreamChunkExtensionKey preserves each complete official Chat
	// Completions stream chunk.
	StreamChunkExtensionKey = "openai/stream_chunk"
)

// ChatCompletionsConfig configures an OpenAI Chat Completions adapter.
// DefaultOptions are copied during construction; callers may select the model
// per request.
type ChatCompletionsConfig struct {
	APIKey         string
	DefaultOptions corechat.Options
	BaseURL        string
	HTTPClient     *http.Client
	Headers        http.Header
}

func (c ChatCompletionsConfig) Validate() error {
	if c.APIKey == "" {
		return errors.New("openai: API key is required")
	}
	if err := c.DefaultOptions.Validate(); err != nil {
		return fmt.Errorf("openai: default options: %w", err)
	}
	return nil
}

var (
	_ corechat.Model    = (*ChatCompletions)(nil)
	_ corechat.Streamer = (*ChatCompletions)(nil)
)

// ChatCompletions implements OpenAI's Chat Completions protocol and is also the
// reusable protocol base for provider packages exposing a compatible endpoint.
type ChatCompletions struct {
	api      *api
	defaults corechat.Options
	dialect  Dialect
}

// NewChatCompletions rejects an invalid provider binding before the first Chat Completions call.
func NewChatCompletions(_ context.Context, config ChatCompletionsConfig) (*ChatCompletions, error) {
	return newChatCompletions(config, Dialect{Provider: protocolProvider, TokenLimitField: TokenLimitMaxCompletionTokens})
}

// NewCompatibleChatCompletions rejects an invalid compatible binding before the first call.
func NewCompatibleChatCompletions(_ context.Context, config ChatCompletionsConfig, dialect Dialect) (*ChatCompletions, error) {
	return newChatCompletions(config, dialect)
}

func newChatCompletions(config ChatCompletionsConfig, dialect Dialect) (*ChatCompletions, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if err := dialect.Validate(); err != nil {
		return nil, fmt.Errorf("openai: dialect: %w", err)
	}
	api, err := newAPI(apiConfig{
		APIKey:     config.APIKey,
		BaseURL:    config.BaseURL,
		HTTPClient: config.HTTPClient,
		Headers:    config.Headers,
	})
	if err != nil {
		return nil, err
	}
	return &ChatCompletions{
		api:      api,
		defaults: config.DefaultOptions.Clone(),
		dialect:  dialect,
	}, nil
}

func (c *ChatCompletions) Call(ctx context.Context, req *corechat.Request) (*corechat.Response, error) {
	params, err := c.buildRequest(req, false)
	if err != nil {
		return nil, err
	}
	response, err := c.api.chatCompletion(ctx, params)
	if err != nil {
		return nil, err
	}
	return mapCompletion(params, response, c.dialect)
}

// Stream performs one streaming Chat Completions request. Stable tool identity
// is retained in adapter-local state until each incomplete wire delta can be
// expressed as a Core response delta.
func (c *ChatCompletions) Stream(ctx context.Context, req *corechat.Request) iter.Seq2[*corechat.ResponseDelta, error] {
	return func(yield func(*corechat.ResponseDelta, error) bool) {
		params, err := c.buildRequest(req, true)
		if err != nil {
			yield(nil, err)
			return
		}

		stream, err := c.api.chatCompletionStream(ctx, params)
		if err != nil {
			yield(nil, err)
			return
		}
		defer stream.Close()

		state := newOpenAIStreamState(c.dialect)
		var terminal *corechat.ResponseDelta
		for stream.Next() {
			response, mapErr := state.mapChunk(stream.Current())
			if mapErr != nil {
				yield(nil, mapErr)
				return
			}
			if terminal != nil {
				if !yield(terminal, nil) {
					return
				}
				terminal = response
				continue
			}
			if state.finished() {
				terminal = response
				continue
			}
			if !yield(response, nil) {
				return
			}
		}
		if streamErr := stream.Err(); streamErr != nil {
			yield(nil, c.api.wrapError(streamErr))
			return
		}
		terminal, err = state.complete(terminal)
		if err != nil {
			yield(nil, err)
			return
		}
		yield(terminal, nil)
	}
}

func (c *ChatCompletions) buildRequest(req *corechat.Request, stream bool) (*openaisdk.ChatCompletionNewParams, error) {
	if c == nil || c.api == nil {
		return nil, errors.New("openai: nil ChatCompletions")
	}
	if err := req.Validate(); err != nil {
		return nil, fmt.Errorf("openai: request: %w", err)
	}
	params := openaisdk.ChatCompletionNewParams{}
	if err := c.applyRequestExtension(req, &params); err != nil {
		return nil, err
	}
	options, err := c.defaults.Resolve(req.Options)
	if err != nil {
		return nil, fmt.Errorf("openai: options: %w", err)
	}
	if applyErr := c.applyOptions(options, req.ToolChoice, &params); applyErr != nil {
		return nil, applyErr
	}

	params.Messages, err = mapRequestMessages(req.Messages)
	if err != nil {
		return nil, err
	}
	params.Tools, err = mapToolDefinitions(req.Tools)
	if err != nil {
		return nil, err
	}
	if formatErr := applyChatOutputFormat(options.OutputFormat, &params, c.dialect); formatErr != nil {
		return nil, formatErr
	}
	if prepareErr := c.prepareRequest(req, stream, &params); prepareErr != nil {
		return nil, prepareErr
	}
	return &params, nil
}

func (c *ChatCompletions) applyRequestExtension(req *corechat.Request, params *openaisdk.ChatCompletionNewParams) error {
	if c.dialect.DisableRawRequestExtension {
		return nil
	}

	extensionKey := protocolRequestExtensionKey(c.dialect.Provider)
	fields, err := decodeRequestFields(req.Options.Extensions, extensionKey,
		"model", "messages", "tools", "frequency_penalty", "max_tokens",
		"max_completion_tokens", "parallel_tool_calls", "presence_penalty", "reasoning_effort", "response_format", "stop", "temperature", "tool_choice", "top_p",
	)
	if err != nil {
		return err
	}
	if _, exists := fields["n"]; exists {
		return fmt.Errorf("openai: extension %q field %q is unsupported; Core Chat produces one output", extensionKey, "n")
	}
	params.SetExtraFields(fields)
	return nil
}

func (c *ChatCompletions) applyOptions(options corechat.Options, toolChoice *corechat.ToolChoice, params *openaisdk.ChatCompletionNewParams) error {
	if options.Model == "" {
		return errors.New("openai: model is required in defaults or request options")
	}
	if options.TopK != nil {
		return errors.New("openai: options.top_k is not supported by Chat Completions")
	}
	params.Model = openaisdk.ChatModel(options.Model)
	if options.FrequencyPenalty != nil {
		if c.dialect.ignores(ChatOptionFrequencyPenalty) {
			return newIgnoredOptionError(c.dialect.Provider, ChatOptionFrequencyPenalty)
		}
		params.FrequencyPenalty = openaisdk.Float(*options.FrequencyPenalty)
	}
	if err := c.applyTokenLimit(options.MaxOutputTokens, params); err != nil {
		return err
	}
	if options.PresencePenalty != nil {
		if c.dialect.ignores(ChatOptionPresencePenalty) {
			return newIgnoredOptionError(c.dialect.Provider, ChatOptionPresencePenalty)
		}
		params.PresencePenalty = openaisdk.Float(*options.PresencePenalty)
	}
	if options.ReasoningEffort != "" && c.dialect.ignores(ChatOptionReasoningEffort) {
		return newIgnoredOptionError(c.dialect.Provider, ChatOptionReasoningEffort)
	}
	reasoningEffort, err := mapReasoningEffort(options.ReasoningEffort)
	if err != nil {
		return err
	}
	params.ReasoningEffort = reasoningEffort
	if len(options.Stop) > 0 {
		params.Stop.OfStringArray = slices.Clone(options.Stop)
	}
	if options.Temperature != nil {
		if limit := c.dialect.MaxTemperature; limit != nil && *options.Temperature > *limit {
			return fmt.Errorf("openai: %s documents a temperature range up to %g, so %v is refused rather than sent out of range",
				c.dialect.Provider, *limit, *options.Temperature)
		}
		params.Temperature = openaisdk.Float(*options.Temperature)
	}
	if options.TopP != nil {
		params.TopP = openaisdk.Float(*options.TopP)
	}
	if toolChoice != nil {
		switch toolChoice.Mode {
		case corechat.ToolChoiceAuto, corechat.ToolChoiceNone, corechat.ToolChoiceRequired:
			params.ToolChoice.OfAuto = openaisdk.String(string(toolChoice.Mode))
		case corechat.ToolChoiceNamed:
			params.ToolChoice = openaisdk.ToolChoiceOptionFunctionToolChoice(
				openaisdk.ChatCompletionNamedToolChoiceFunctionParam{Name: toolChoice.Name},
			)
		}
		switch toolChoice.Parallelism {
		case corechat.ToolParallelismAllow:
			params.ParallelToolCalls = openaisdk.Bool(true)
		case corechat.ToolParallelismSingle:
			params.ParallelToolCalls = openaisdk.Bool(false)
		}
	}
	return nil
}

func (c *ChatCompletions) applyTokenLimit(limit *int64, params *openaisdk.ChatCompletionNewParams) error {
	if limit == nil {
		return nil
	}
	switch c.dialect.TokenLimitField {
	case TokenLimitMaxTokens:
		params.MaxTokens = openaisdk.Int(*limit)
	case TokenLimitMaxCompletionTokens:
		params.MaxCompletionTokens = openaisdk.Int(*limit)
	default:
		return errors.New("openai: invalid max token field configuration")
	}
	return nil
}

func (c *ChatCompletions) prepareRequest(req *corechat.Request, stream bool, params *openaisdk.ChatCompletionNewParams) error {
	if c.dialect.request != nil {
		if err := c.dialect.request.PrepareRequest(req, params); err != nil {
			return fmt.Errorf("openai: request dialect: %w", err)
		}
	}
	if c.dialect.PrepareRequest == nil {
		return nil
	}

	compatible := &CompatibleRequest{
		model:       string(params.Model),
		stream:      stream,
		extraFields: maps.Clone(params.ExtraFields()),
	}
	if params.Temperature.Valid() {
		compatible.temperature = &params.Temperature.Value
	}
	if params.TopP.Valid() {
		compatible.topP = &params.TopP.Value
	}
	if err := c.dialect.PrepareRequest(req, compatible); err != nil {
		return fmt.Errorf("openai: compatible request dialect: %w", err)
	}
	params.SetExtraFields(compatible.extraFields)
	return nil
}
