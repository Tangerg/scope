package azureopenai

import (
	"fmt"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/models/protocol/openai"
)

// Namespacing preserves provider-specific data without promoting it into the
// shared Core protocol or colliding with another provider.
const (
	OpenAIRequestExtensionKey     = "azureopenai/openai_request"
	OpenAIResponseExtensionKey    = "azureopenai/openai_response"
	OpenAIStreamChunkExtensionKey = "azureopenai/openai_stream_chunk"
)

var (
	_ corechat.Model    = (*Chat)(nil)
	_ corechat.Streamer = (*Chat)(nil)
)

// Chat implements Azure OpenAI's Chat Completions protocol.
type Chat = openai.ChatCompletions

// ChatConfig binds provider access and defaults shared by every chat call.
type ChatConfig struct {
	Config
	DefaultOptions corechat.Options
}

func (c ChatConfig) resolve() (endpointConfig, error) {
	return resolveChatConfig(c.Config, c.DefaultOptions.Validate)
}

func (c ChatConfig) Validate() error {
	_, err := c.resolve()
	return err
}

// NewChat rejects an invalid provider binding before the first chat call.
func NewChat(config ChatConfig) (*Chat, error) {
	endpoint, err := config.resolve()
	if err != nil {
		return nil, err
	}
	protocol, err := openai.NewCompatibleChatCompletions(openai.ChatCompletionsConfig{APIKey: endpoint.apiKey, DefaultOptions: config.DefaultOptions, BaseURL: endpoint.baseURL, HTTPClient: endpoint.httpClient}, openai.Dialect{
		Provider: protocolProvider,
		// Azure documents that reasoning models "will only work with the
		// max_completion_tokens parameter when using the Chat Completions
		// API", and lists max_tokens among the parameters those models do
		// not support. This adapter targets the current v1 endpoint with no
		// dated api-version, which is the surface where the replacement is
		// the documented field — so max_tokens was the one field a caller's
		// MaxOutputTokens could not survive on a GPT-5 or o-series
		// deployment. models/openai speaks the same protocol and already
		// sends the replacement.
		TokenLimitField: openai.TokenLimitMaxCompletionTokens,
	})
	if err != nil {
		return nil, fmt.Errorf("azureopenai: construct chat: %w", err)
	}
	return protocol, nil
}
