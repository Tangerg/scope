package minimax

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/models/protocol/anthropic"
	"github.com/Tangerg/scope/models/protocol/openai"
)

// Namespacing preserves provider-specific data without promoting it into the
// shared Core protocol or colliding with another provider.
const (
	OpenAIRequestExtensionKey        = "minimax/openai_request"
	OpenAIResponseExtensionKey       = "minimax/openai_response"
	OpenAIStreamChunkExtensionKey    = "minimax/openai_stream_chunk"
	AnthropicRequestExtensionKey     = "minimax/anthropic_request"
	AnthropicResponseExtensionKey    = "minimax/anthropic_response"
	AnthropicStreamEventExtensionKey = "minimax/anthropic_stream_event"
)

var (
	_ corechat.Model    = (*Chat)(nil)
	_ corechat.Streamer = (*Chat)(nil)
	_ corechat.Model    = (*Messages)(nil)
	_ corechat.Streamer = (*Messages)(nil)
)

// Chat implements MiniMax's OpenAI-compatible endpoint.
type Chat = openai.ChatCompletions

// Messages implements MiniMax's Anthropic-compatible endpoint.
type Messages = anthropic.Messages

// ChatConfig binds provider access and defaults shared by every chat call.
type ChatConfig struct {
	APIKey         string
	DefaultOptions corechat.Options
	BaseURL        string
	HTTPClient     *http.Client
}

func (c ChatConfig) Validate() error {
	if c.APIKey == "" {
		return errors.New("minimax: APIKey is required")
	}
	if err := c.DefaultOptions.Validate(); err != nil {
		return fmt.Errorf("minimax: DefaultOptions: %w", err)
	}
	return nil
}

// NewChat rejects an invalid provider binding before the first chat call.
func NewChat(ctx context.Context, config ChatConfig) (*Chat, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	reasoningDialect, err := openai.ReasoningDetailsDialect(openai.ReasoningDetailsConfig{
		Provider:     "minimax",
		TextField:    "reasoning_content",
		DetailsField: "reasoning_details",
	})
	if err != nil {
		return nil, fmt.Errorf("minimax: configure reasoning dialect: %w", err)
	}
	reasoningDialect.PrepareRequest = prepareOpenAIRequest
	reasoningDialect.TokenLimitField = openai.TokenLimitMaxCompletionTokens
	protocol, err := openai.NewCompatibleChatCompletions(ctx,
		openai.ChatCompletionsConfig{APIKey: config.APIKey, DefaultOptions: config.DefaultOptions, BaseURL: cmp.Or(config.BaseURL, BaseURLIntl), HTTPClient: config.HTTPClient},
		reasoningDialect,
	)
	if err != nil {
		return nil, fmt.Errorf("minimax: construct OpenAI-compatible chat: %w", err)
	}
	return protocol, nil
}

// MessagesConfig binds provider access and defaults shared by every Messages call.
type MessagesConfig struct {
	APIKey         string
	DefaultOptions corechat.Options
	BaseURL        string
	HTTPClient     *http.Client
}

func (m MessagesConfig) Validate() error {
	if m.APIKey == "" {
		return errors.New("minimax: APIKey is required")
	}
	if err := m.DefaultOptions.Validate(); err != nil {
		return fmt.Errorf("minimax: DefaultOptions: %w", err)
	}
	return nil
}

// NewMessages rejects an invalid provider binding before the first Messages call.
func NewMessages(ctx context.Context, config MessagesConfig) (*Messages, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	protocol, err := anthropic.NewCompatibleMessages(ctx, anthropic.MessagesConfig{APIKey: config.APIKey, DefaultOptions: config.DefaultOptions, BaseURL: cmp.Or(config.BaseURL, BaseURLIntlAnthropic), HTTPClient: config.HTTPClient}, anthropic.Dialect{Provider: "minimax"})
	if err != nil {
		return nil, fmt.Errorf("minimax: construct Anthropic-compatible chat: %w", err)
	}
	return protocol, nil
}
