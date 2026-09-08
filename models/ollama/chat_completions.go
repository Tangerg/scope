package ollama

import (
	"cmp"
	"context"
	"fmt"
	"net/http"
	"strings"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/models/protocol/openai"
)

// Namespacing preserves provider-specific data without promoting it into the
// shared Core protocol or colliding with another provider.
const (
	OpenAIRequestExtensionKey     = "ollama/openai_request"
	OpenAIResponseExtensionKey    = "ollama/openai_response"
	OpenAIStreamChunkExtensionKey = "ollama/openai_stream_chunk"
)

var (
	_ corechat.Model    = (*ChatCompletions)(nil)
	_ corechat.Streamer = (*ChatCompletions)(nil)
)

// ChatCompletions implements Ollama's OpenAI-compatible endpoint.
type ChatCompletions = openai.ChatCompletions

// ChatCompletionsConfig binds provider access and defaults shared by every Chat Completions call.
type ChatCompletionsConfig struct {
	APIKey         string
	DefaultOptions corechat.Options
	BaseURL        string
	HTTPClient     *http.Client
}

func (c ChatCompletionsConfig) Validate() error {
	if err := c.DefaultOptions.Validate(); err != nil {
		return fmt.Errorf("ollama: DefaultOptions: %w", err)
	}
	return nil
}

// NewChatCompletions rejects an invalid provider binding before the first Chat Completions call.
func NewChatCompletions(ctx context.Context, config ChatCompletionsConfig) (*ChatCompletions, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	apiKey := config.APIKey
	if apiKey == "" {
		apiKey = "ollama"
	}
	protocol, err := openai.NewCompatibleChatCompletions(ctx, openai.ChatCompletionsConfig{APIKey: apiKey, DefaultOptions: config.DefaultOptions, BaseURL: resolveOpenAIBaseURL(config.BaseURL), HTTPClient: config.HTTPClient}, openai.Dialect{Provider: "ollama", TokenLimitField: openai.TokenLimitMaxTokens})
	if err != nil {
		return nil, fmt.Errorf("ollama: construct OpenAI-compatible chat: %w", err)
	}
	return protocol, nil
}

func resolveOpenAIBaseURL(base string) string {
	base = strings.TrimRight(cmp.Or(base, DefaultBaseURL), "/")
	if strings.HasSuffix(base, OpenAICompatPath) {
		return base
	}
	return base + OpenAICompatPath
}
