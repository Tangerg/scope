package moonshot

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
	OpenAIRequestExtensionKey        = "moonshot/openai_request"
	OpenAIResponseExtensionKey       = "moonshot/openai_response"
	OpenAIStreamChunkExtensionKey    = "moonshot/openai_stream_chunk"
	AnthropicRequestExtensionKey     = "moonshot/anthropic_request"
	AnthropicResponseExtensionKey    = "moonshot/anthropic_response"
	AnthropicStreamEventExtensionKey = "moonshot/anthropic_stream_event"
)

var (
	_ corechat.Model    = (*Chat)(nil)
	_ corechat.Streamer = (*Chat)(nil)
	_ corechat.Model    = (*Messages)(nil)
	_ corechat.Streamer = (*Messages)(nil)
)

// Chat implements Moonshot's OpenAI-compatible endpoint.
type Chat = openai.ChatCompletions

// Messages implements Moonshot's Anthropic-compatible endpoint.
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
		return errors.New("moonshot: APIKey is required")
	}
	if err := c.DefaultOptions.Validate(); err != nil {
		return fmt.Errorf("moonshot: DefaultOptions: %w", err)
	}
	return nil
}

// maximumTemperature is the top of the range Moonshot documents for its
// OpenAI-compatible endpoint.
var maximumTemperature = 1.0

// NewChat rejects an invalid provider binding before the first chat call.
func NewChat(ctx context.Context, config ChatConfig) (*Chat, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	dialect := openai.ReasoningContentReplayDialect("moonshot")
	dialect.PrepareRequest = prepareOpenAIRequest
	dialect.TokenLimitField = openai.TokenLimitMaxCompletionTokens
	// Moonshot's migration guide states the narrower range outright: "Kimi API
	// 的 temperature 参数的取值范围是 [0, 1]，而 OpenAI 的 temperature 参数的
	// 取值范围是 [0, 2]". Refusing above it beats sending a value the provider
	// documents as out of range.
	dialect.MaxTemperature = &maximumTemperature
	protocol, err := openai.NewCompatibleChatCompletions(ctx, openai.ChatCompletionsConfig{APIKey: config.APIKey, DefaultOptions: config.DefaultOptions, BaseURL: cmp.Or(config.BaseURL, BaseURL), HTTPClient: config.HTTPClient}, dialect)
	if err != nil {
		return nil, fmt.Errorf("moonshot: construct OpenAI-compatible chat: %w", err)
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
		return errors.New("moonshot: APIKey is required")
	}
	if err := m.DefaultOptions.Validate(); err != nil {
		return fmt.Errorf("moonshot: DefaultOptions: %w", err)
	}
	return nil
}

// NewMessages rejects an invalid provider binding before the first Messages call.
func NewMessages(ctx context.Context, config MessagesConfig) (*Messages, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	protocol, err := anthropic.NewCompatibleMessages(ctx, anthropic.MessagesConfig{APIKey: config.APIKey, DefaultOptions: config.DefaultOptions, BaseURL: cmp.Or(config.BaseURL, BaseURLAnthropic), HTTPClient: config.HTTPClient}, anthropic.Dialect{Provider: "moonshot"})
	if err != nil {
		return nil, fmt.Errorf("moonshot: construct Anthropic-compatible chat: %w", err)
	}
	return protocol, nil
}
