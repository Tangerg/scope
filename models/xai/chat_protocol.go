package xai

import (
	"cmp"
	"context"
	"fmt"
	"net/http"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/models/protocol/openai"
)

// Namespacing preserves provider-specific data without promoting it into the
// shared Core protocol or colliding with another provider.
const (
	OpenAIRequestExtensionKey     = Provider + openai.CompatibleRequestExtensionSuffix
	OpenAIStreamChunkExtensionKey = Provider + openai.CompatibleStreamExtensionSuffix
)

type ChatCompletionsConfig struct {
	APIKey         string
	DefaultOptions corechat.Options
	BaseURL        string
	HTTPClient     *http.Client
}

func (c ChatCompletionsConfig) Validate() error {
	if err := c.protocol().Validate(); err != nil {
		return fmt.Errorf("xai: %w", err)
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
		TokenLimitField: openai.TokenLimitMaxCompletionTokens,
		TextReasoning:   &openai.TextReasoningConfig{Fields: []openai.TextReasoningField{openai.TextReasoningContent}, Replay: openai.TextReasoningReplayNever},
	}
	protocol, err := openai.NewChatCompletions(ctx, config.protocol(), dialect)
	if err != nil {
		return nil, fmt.Errorf("xai: construct OpenAI-compatible chat: %w", err)
	}
	return corechat.NewStreamModel(protocol)
}
