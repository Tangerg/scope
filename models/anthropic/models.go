package anthropic

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"

	corechat "github.com/Tangerg/scope/core/chat"
	anthropicprotocol "github.com/Tangerg/scope/models/protocol/anthropic"
	openaiprotocol "github.com/Tangerg/scope/models/protocol/openai"
)

// Exported identifiers keep provider-owned names and defaults out of caller literals.
const (
	Provider      = "Anthropic"
	BaseURLOpenAI = "https://api.anthropic.com/v1"

	OpenAIRequestExtensionKey     = "anthropic/openai_request"
	OpenAIResponseExtensionKey    = "anthropic/openai_response"
	OpenAIStreamChunkExtensionKey = "anthropic/openai_stream_chunk"
)

// ChatConfig binds provider access and defaults shared by every chat call.
type ChatConfig struct {
	APIKey         string
	DefaultOptions corechat.Options
	BaseURL        string
	HTTPClient     *http.Client
}

func (c ChatConfig) Validate() error { return c.protocol().Validate() }

func (c ChatConfig) protocol() anthropicprotocol.MessagesConfig {
	return anthropicprotocol.MessagesConfig{APIKey: c.APIKey, DefaultOptions: c.DefaultOptions, BaseURL: c.BaseURL, HTTPClient: c.HTTPClient}
}

// Chat is the Anthropic Messages protocol model.
type Chat = anthropicprotocol.Messages

// NewChat rejects an invalid provider binding before the first chat call.
func NewChat(ctx context.Context, config ChatConfig) (*Chat, error) {
	return anthropicprotocol.NewMessages(ctx, config.protocol())
}

// ChatCompletionsConfig binds provider access and defaults shared by every Chat Completions call.
type ChatCompletionsConfig struct {
	APIKey         string
	DefaultOptions corechat.Options
	BaseURL        string
	HTTPClient     *http.Client
}

func (c ChatCompletionsConfig) Validate() error {
	if c.APIKey == "" {
		return errors.New("anthropic: APIKey is required")
	}
	if err := c.DefaultOptions.Validate(); err != nil {
		return fmt.Errorf("anthropic: DefaultOptions: %w", err)
	}
	return nil
}

// ChatCompletions implements Anthropic's OpenAI-compatible endpoint.
type ChatCompletions = openaiprotocol.ChatCompletions

// NewChatCompletions rejects an invalid provider binding before the first Chat Completions call.
func NewChatCompletions(ctx context.Context, config ChatCompletionsConfig) (*ChatCompletions, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	return openaiprotocol.NewCompatibleChatCompletions(ctx,
		openaiprotocol.ChatCompletionsConfig{APIKey: config.APIKey, DefaultOptions: config.DefaultOptions, BaseURL: cmp.Or(config.BaseURL, BaseURLOpenAI), HTTPClient: config.HTTPClient},
		openaiprotocol.Dialect{Provider: "anthropic", TokenLimitField: openaiprotocol.TokenLimitMaxTokens},
	)
}

// TextEstimatorConfig binds provider access and the model shared by every estimate.
type TextEstimatorConfig struct {
	APIKey     string
	Model      string
	BaseURL    string
	HTTPClient *http.Client
}

func (t TextEstimatorConfig) Validate() error { return t.protocol().Validate() }

func (t TextEstimatorConfig) protocol() anthropicprotocol.TextEstimatorConfig {
	return anthropicprotocol.TextEstimatorConfig{APIKey: t.APIKey, Model: t.Model, BaseURL: t.BaseURL, HTTPClient: t.HTTPClient}
}

// TextEstimator is Anthropic's token-counting estimator.
type TextEstimator = anthropicprotocol.TextEstimator

// NewTextEstimator rejects an invalid provider/model binding before estimation begins.
func NewTextEstimator(ctx context.Context, config TextEstimatorConfig) (*TextEstimator, error) {
	return anthropicprotocol.NewTextEstimator(ctx, config.protocol())
}
