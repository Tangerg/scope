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
	OpenAIStreamChunkExtensionKey = "anthropic/openai_stream_chunk"
)

// MessagesConfig binds provider access and defaults shared by every chat call.
type MessagesConfig struct {
	APIKey         string
	DefaultOptions corechat.Options
	BaseURL        string
	HTTPClient     *http.Client
}

func (m MessagesConfig) Validate() error { return m.protocol().Validate() }

func (m MessagesConfig) protocol() anthropicprotocol.MessagesConfig {
	return anthropicprotocol.MessagesConfig{APIKey: m.APIKey, DefaultOptions: m.DefaultOptions, BaseURL: m.BaseURL, HTTPClient: m.HTTPClient}
}

// NewMessages rejects an invalid provider binding before the first chat call.
func NewMessages(ctx context.Context, config MessagesConfig) (*anthropicprotocol.Messages, error) {
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

// NewChatCompletions rejects an invalid provider binding before the first Chat Completions call.
func NewChatCompletions(ctx context.Context, config ChatCompletionsConfig) (*openaiprotocol.ChatCompletions, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	// Anthropic publishes a field-by-field support table for this endpoint, and
	// several entries a Core option maps onto say "Ignored". Sending one and
	// letting Anthropic discard it is, to the caller, the same as this adapter
	// never mapping it, so each is refused instead. Temperature is the same
	// problem in another shape: the table caps it at 1 and says "values greater
	// than 1 are capped at 1", which alters the request rather than rejecting
	// it. response_format is also ignored, so JSON output goes through the
	// shared prompt fallback rather than a parameter the endpoint drops.
	maximumTemperature := 1.0
	return openaiprotocol.NewCompatibleChatCompletions(ctx,
		openaiprotocol.ChatCompletionsConfig{APIKey: config.APIKey, DefaultOptions: config.DefaultOptions, BaseURL: cmp.Or(config.BaseURL, BaseURLOpenAI), HTTPClient: config.HTTPClient},
		openaiprotocol.Dialect{
			Provider:        "anthropic",
			TokenLimitField: openaiprotocol.TokenLimitMaxTokens,
			IgnoredOptions: []openaiprotocol.ChatOption{
				openaiprotocol.ChatOptionFrequencyPenalty,
				openaiprotocol.ChatOptionPresencePenalty,
				openaiprotocol.ChatOptionReasoningEffort,
			},
			MaxTemperature:     &maximumTemperature,
			NativeOutputFormat: func(corechat.OutputFormatType) bool { return false },
		},
	)
}

// TextCounterConfig binds provider access and the model shared by every count.
type TextCounterConfig struct {
	APIKey     string
	Model      string
	BaseURL    string
	HTTPClient *http.Client
}

func (t TextCounterConfig) Validate() error { return t.protocol().Validate() }

func (t TextCounterConfig) protocol() anthropicprotocol.TextCounterConfig {
	return anthropicprotocol.TextCounterConfig{APIKey: t.APIKey, Model: t.Model, BaseURL: t.BaseURL, HTTPClient: t.HTTPClient}
}

// NewTextCounter rejects an invalid provider/model binding before counting begins.
func NewTextCounter(ctx context.Context, config TextCounterConfig) (*anthropicprotocol.TextCounter, error) {
	return anthropicprotocol.NewTextCounter(ctx, config.protocol())
}
