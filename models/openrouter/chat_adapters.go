package openrouter

import (
	"cmp"
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"net/http"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/metadata"
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

const (
	reasoningField          = "reasoning"
	reasoningEffortField    = "effort"
	reasoningEnabledField   = "enabled"
	reasoningMaxTokensField = "max_tokens"
)

type ChatCompletionsConfig struct {
	APIKey         string
	DefaultOptions corechat.Options
	BaseURL        string
	AppURL         string
	AppTitle       string
	HTTPClient     *http.Client
}

func (c ChatCompletionsConfig) Validate() error {
	if err := c.protocol().Validate(); err != nil {
		return fmt.Errorf("openrouter: %w", err)
	}
	return nil
}

func (c ChatCompletionsConfig) protocol() openai.ChatCompletionsConfig {
	return openai.ChatCompletionsConfig{
		APIKey: c.APIKey, DefaultOptions: c.DefaultOptions,
		BaseURL: cmp.Or(c.BaseURL, BaseURL), HTTPClient: c.HTTPClient,
		Headers: providerHeaders(c.AppURL, c.AppTitle),
	}
}

func NewChatCompletions(ctx context.Context, config ChatCompletionsConfig) (*corechat.StreamModel, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	dialect := openai.DialectConfig{
		Provider:        Provider,
		PrepareRequest:  prepareOpenAIRequest,
		TokenLimitField: openai.TokenLimitMaxCompletionTokens,
		SupportsTopK:    true,
		ReasoningDetails: &openai.ReasoningDetailsConfig{
			TextField:       "reasoning",
			DetailsField:    "reasoning_details",
			ReplayPlainText: true,
		},
	}
	protocol, err := openai.NewChatCompletions(ctx, config.protocol(), dialect)
	if err != nil {
		return nil, fmt.Errorf("openrouter: construct OpenAI-compatible chat: %w", err)
	}
	return corechat.NewStreamModel(protocol)
}

type MessagesConfig struct {
	APIKey         string
	DefaultOptions corechat.Options
	BaseURL        string
	AppURL         string
	AppTitle       string
	HTTPClient     *http.Client
}

func (m MessagesConfig) Validate() error {
	if err := m.protocol().Validate(); err != nil {
		return fmt.Errorf("openrouter: %w", err)
	}
	return nil
}

func (m MessagesConfig) protocol() anthropic.MessagesConfig {
	return anthropic.MessagesConfig{
		APIKey: m.APIKey, DefaultOptions: m.DefaultOptions,
		BaseURL: cmp.Or(m.BaseURL, BaseURLAnthropic), HTTPClient: m.HTTPClient,
		Headers: providerHeaders(m.AppURL, m.AppTitle),
	}
}

func NewMessages(ctx context.Context, config MessagesConfig) (*corechat.StreamModel, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	protocol, err := anthropic.NewMessages(ctx, config.protocol(), anthropic.Dialect{Provider: Provider, Revision: anthropic.MessagesRevision20230601, APIKeyScheme: anthropic.APIKeySchemeBearer, SupportsSampling: true})
	if err != nil {
		return nil, fmt.Errorf("openrouter: construct Anthropic-compatible chat: %w", err)
	}
	return corechat.NewStreamModel(protocol)
}

func providerHeaders(appURL, appTitle string) http.Header {
	headers := make(http.Header)
	if appURL != "" {
		headers.Set(HeaderReferer, appURL)
	}
	if appTitle != "" {
		headers.Set(HeaderAppTitle, appTitle)
	}
	return headers
}

func prepareOpenAIRequest(source *corechat.Request, _ *openai.CompatibleRequest) error {
	fields, _, err := source.Options.Extensions.Decode[map[string]json.RawMessage](OpenAIRequestExtensionKey)
	if err != nil {
		return fmt.Errorf("openrouter: request extension: %w", err)
	}
	if raw, found := fields[reasoningField]; found {
		var reasoning map[string]json.RawMessage
		if err := jsonv2.Unmarshal(raw, &reasoning); err != nil {
			return fmt.Errorf("openrouter: reasoning: %w", err)
		}
		if _, found := reasoning[reasoningEffortField]; found {
			return fmt.Errorf("openrouter: reasoning.effort belongs to options.reasoning_effort: %w", metadata.ErrCoreOwnedField)
		}
		if _, found := reasoning[reasoningEnabledField]; found {
			return fmt.Errorf("openrouter: reasoning.enabled belongs to options.reasoning_effort: %w", metadata.ErrCoreOwnedField)
		}
		if _, found := reasoning[reasoningMaxTokensField]; found && source.Options.ReasoningEffort != "" {
			return errors.New("openrouter: reasoning.max_tokens cannot be combined with options.reasoning_effort")
		}
	}
	return nil
}
