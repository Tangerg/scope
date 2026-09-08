package openai

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"net/http"

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
