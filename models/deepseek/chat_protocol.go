package deepseek

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
		return fmt.Errorf("deepseek: %w", err)
	}
	options, _, err := c.DefaultOptions.Extensions.Decode[RequestOptions](RequestExtensionKey)
	if err != nil {
		return fmt.Errorf("deepseek: DefaultOptions: %w", err)
	}
	if err := options.ValidateFor(c.DefaultOptions, nil, true); err != nil {
		return fmt.Errorf("deepseek: DefaultOptions: %w", err)
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
		Provider:                   Provider,
		PrepareRequest:             prepareRequest,
		TokenLimitField:            openai.TokenLimitMaxTokens,
		NativeOutputFormat:         nativeOutputFormat,
		DisableRawRequestExtension: true,
		TextReasoning:              &openai.TextReasoningConfig{Fields: []openai.TextReasoningField{openai.TextReasoningContent}, Replay: openai.TextReasoningReplayAlways},
	}
	protocol, err := openai.NewChatCompletions(ctx, config.protocol(), dialect)
	if err != nil {
		return nil, fmt.Errorf("deepseek: construct OpenAI-compatible chat: %w", err)
	}
	return corechat.NewStreamModel(protocol)
}

func nativeOutputFormat(format corechat.OutputFormatType) bool {
	return format == corechat.OutputFormatText || format == corechat.OutputFormatJSON
}
