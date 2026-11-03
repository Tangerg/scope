package anthropic

import (
	"cmp"
	"context"
	"net/http"

	corechat "github.com/Tangerg/scope/core/chat"
	anthropicprotocol "github.com/Tangerg/scope/models/protocol/anthropic"
	openaiprotocol "github.com/Tangerg/scope/models/protocol/openai"
)

const providerID = anthropicprotocol.ProviderID

const (
	Provider      = "Anthropic"
	BaseURLOpenAI = "https://api.anthropic.com/v1"

	OpenAIRequestExtensionKey     = providerID + openaiprotocol.CompatibleRequestExtensionSuffix
	OpenAIStreamChunkExtensionKey = providerID + openaiprotocol.CompatibleStreamExtensionSuffix
)

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

func NewMessages(ctx context.Context, config MessagesConfig) (*anthropicprotocol.Messages, error) {
	return anthropicprotocol.NewMessages(ctx, config.protocol(), anthropicprotocol.Dialect{
		Provider:         providerID,
		Revision:         anthropicprotocol.MessagesRevision20261021,
		APIKeyScheme:     anthropicprotocol.APIKeySchemeBearer,
		NativeJSONSchema: true},
	)
}

type ChatCompletionsConfig struct {
	APIKey         string
	DefaultOptions corechat.Options
	BaseURL        string
	HTTPClient     *http.Client
}

func (c ChatCompletionsConfig) Validate() error { return c.protocol().Validate() }

func (c ChatCompletionsConfig) protocol() openaiprotocol.ChatCompletionsConfig {
	return openaiprotocol.ChatCompletionsConfig{APIKey: c.APIKey, DefaultOptions: c.DefaultOptions, BaseURL: cmp.Or(c.BaseURL, BaseURLOpenAI), HTTPClient: c.HTTPClient}
}

func NewChatCompletions(ctx context.Context, config ChatCompletionsConfig) (*corechat.StreamModel, error) {
	// Anthropic publishes a field-by-field support table for this endpoint, and
	// several entries a Core option maps onto say "Ignored". Sending one and
	// letting Anthropic discard it is, to the caller, the same as this adapter
	// never mapping it, so each is refused instead. Temperature is the same
	// problem in another shape: the table caps it at 1 and says "values greater
	// than 1 are capped at 1", which alters the request rather than rejecting
	// it. response_format is also ignored, so JSON output goes through the
	// ErrUnsupportedOutputFormat instead of a parameter the endpoint drops.
	maximumTemperature := 1.0
	protocol, err := openaiprotocol.NewChatCompletions(ctx,
		config.protocol(),
		openaiprotocol.DialectConfig{
			Provider:        providerID,
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
	if err != nil {
		return nil, err
	}
	return corechat.NewStreamModel(protocol)
}
