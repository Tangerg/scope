package groq

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
		return fmt.Errorf("groq: %w", err)
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
	// Groq's API reference marks max_tokens "Deprecated in favor of
	// max_completion_tokens", and its own text-generation guide passes the
	// replacement. The reasoning dialect defaults to the legacy field, which is the
	// wrong half of that deprecation for a provider whose models reason: the
	// replacement is documented as bounding reasoning tokens together with
	// visible output, so it is the field that caps what this provider actually
	// generates.
	dialect := openai.DialectConfig{
		Provider:           Provider,
		TokenLimitField:    openai.TokenLimitMaxCompletionTokens,
		TextReasoning:      &openai.TextReasoningConfig{Fields: []openai.TextReasoningField{openai.TextReasoning}, Replay: openai.TextReasoningReplayNever},
		NativeOutputFormat: nativeOutputFormat,
	}
	protocol, err := openai.NewChatCompletions(ctx, config.protocol(), dialect)
	if err != nil {
		return nil, fmt.Errorf("groq: construct OpenAI-compatible chat: %w", err)
	}
	return corechat.NewStreamModel(protocol)
}

// nativeOutputFormat excludes json_schema, which Groq does not stream; Call
// aggregates the same stream, so this adapter cannot promise it either way.
func nativeOutputFormat(format corechat.OutputFormatType) bool {
	return format != corechat.OutputFormatJSONSchema
}
