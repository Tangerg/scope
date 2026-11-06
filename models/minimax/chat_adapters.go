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
	if c.APIKey == "" {
		return errors.New("minimax: APIKey is required")
	}
	if err := c.DefaultOptions.Validate(); err != nil {
		return fmt.Errorf("minimax: DefaultOptions: %w", err)
	}
	return nil
}

func NewChatCompletions(ctx context.Context, config ChatCompletionsConfig) (*corechat.StreamModel, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	reasoningDialect := openai.DialectConfig{Provider: Provider, TokenLimitField: openai.TokenLimitMaxTokens, ReasoningDetails: &openai.ReasoningDetailsConfig{
		TextField:       "reasoning_content",
		DetailsField:    "reasoning_details",
		ReplayPlainText: true,
	}}
	reasoningDialect.PrepareRequest = prepareOpenAIRequest
	reasoningDialect.DisableRawRequestExtension = true
	reasoningDialect.TokenLimitField = openai.TokenLimitMaxCompletionTokens
	reasoningDialect.IgnoredOptions = []openai.ChatOption{
		openai.ChatOptionTemperature, openai.ChatOptionTopP,
		openai.ChatOptionPresencePenalty, openai.ChatOptionFrequencyPenalty,
	}
	protocol, err := openai.NewChatCompletions(ctx,
		openai.ChatCompletionsConfig{APIKey: config.APIKey, DefaultOptions: config.DefaultOptions, BaseURL: cmp.Or(config.BaseURL, BaseURLIntl), HTTPClient: config.HTTPClient},
		reasoningDialect,
	)
	if err != nil {
		return nil, fmt.Errorf("minimax: construct OpenAI-compatible chat: %w", err)
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
	if m.APIKey == "" {
		return errors.New("minimax: APIKey is required")
	}
	if err := m.DefaultOptions.Validate(); err != nil {
		return fmt.Errorf("minimax: DefaultOptions: %w", err)
	}
	return nil
}

func NewMessages(ctx context.Context, config MessagesConfig) (*corechat.StreamModel, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	protocol, err := anthropic.NewMessages(ctx, anthropic.MessagesConfig{APIKey: config.APIKey, DefaultOptions: config.DefaultOptions, BaseURL: cmp.Or(config.BaseURL, BaseURLIntlAnthropic), HTTPClient: config.HTTPClient}, anthropic.Dialect{Provider: Provider, Revision: anthropic.MessagesRevision20230601, APIKeyScheme: anthropic.APIKeySchemeXAPIKey, SupportsSampling: true, PrepareRequest: prepareAnthropicRequest})
	if err != nil {
		return nil, fmt.Errorf("minimax: construct Anthropic-compatible chat: %w", err)
	}
	return corechat.NewStreamModel(protocol)
}
