package xiaomi

import (
	"cmp"
	"context"
	"fmt"
	"net/http"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/models/protocol/anthropic"
	"github.com/Tangerg/scope/models/protocol/openai"
)

// Namespacing preserves provider-specific data without promoting it into the
// shared Core protocol or colliding with another provider.
const (
	OpenAIRequestExtensionKey        = Provider + openai.CompatibleRequestExtensionSuffix
	OpenAIStreamChunkExtensionKey    = Provider + openai.CompatibleStreamExtensionSuffix
	AnthropicRequestExtensionKey     = Provider + anthropic.CompatibleRequestExtensionSuffix
	AnthropicStreamEventExtensionKey = Provider + anthropic.CompatibleStreamExtensionSuffix
)

type ChatCompletionsConfig struct {
	APIKey         string
	DefaultOptions corechat.Options
	BaseURL        string
	HTTPClient     *http.Client
}

func (c ChatCompletionsConfig) Validate() error {
	if err := c.protocol().Validate(); err != nil {
		return fmt.Errorf("xiaomi: %w", err)
	}
	return nil
}

func (c ChatCompletionsConfig) protocol() openai.ChatCompletionsConfig {
	return openai.ChatCompletionsConfig{APIKey: c.APIKey, DefaultOptions: c.DefaultOptions, BaseURL: cmp.Or(c.BaseURL, BaseURL), HTTPClient: c.HTTPClient}
}

func NewChatCompletions(ctx context.Context, config ChatCompletionsConfig) (*corechat.StreamModel, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	dialect := openai.DialectConfig{
		Provider:        Provider,
		PrepareRequest:  prepareOpenAIRequest,
		TokenLimitField: openai.TokenLimitMaxCompletionTokens,
		TextReasoning:   &openai.TextReasoningConfig{Fields: []openai.TextReasoningField{openai.TextReasoningContent}, Replay: openai.TextReasoningReplayWithToolCalls},
	}
	protocol, err := openai.NewChatCompletions(ctx, config.protocol(), dialect)
	if err != nil {
		return nil, fmt.Errorf("xiaomi: construct OpenAI-compatible chat: %w", err)
	}
	return corechat.NewStreamModel(protocol)
}

type MessagesConfig struct {
	APIKey         string
	DefaultOptions corechat.Options
	BaseURL        string
	HTTPClient     *http.Client
}

func (m MessagesConfig) Validate() error {
	if err := m.protocol().Validate(); err != nil {
		return fmt.Errorf("xiaomi: %w", err)
	}
	return nil
}

func (m MessagesConfig) protocol() anthropic.MessagesConfig {
	return anthropic.MessagesConfig{APIKey: m.APIKey, DefaultOptions: m.DefaultOptions, BaseURL: cmp.Or(m.BaseURL, BaseURLAnthropic), HTTPClient: m.HTTPClient}
}

func NewMessages(ctx context.Context, config MessagesConfig) (*corechat.StreamModel, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	protocol, err := anthropic.NewMessages(ctx, config.protocol(), anthropic.Dialect{Provider: Provider, Revision: anthropic.MessagesRevision20230601, APIKeyScheme: anthropic.APIKeySchemeAPIKey, SupportsSampling: true, PrepareRequest: prepareAnthropicRequest})
	if err != nil {
		return nil, fmt.Errorf("xiaomi: construct Anthropic-compatible chat: %w", err)
	}
	return corechat.NewStreamModel(protocol)
}
