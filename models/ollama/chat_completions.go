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
	if err := c.DefaultOptions.Validate(); err != nil {
		return fmt.Errorf("ollama: DefaultOptions: %w", err)
	}
	return nil
}

func NewChatCompletions(ctx context.Context, config ChatCompletionsConfig) (*corechat.StreamModel, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	apiKey := config.APIKey
	if apiKey == "" {
		apiKey = "ollama"
	}
	protocol, err := openai.NewChatCompletions(ctx, openai.ChatCompletionsConfig{APIKey: apiKey, DefaultOptions: config.DefaultOptions, BaseURL: resolveOpenAIBaseURL(config.BaseURL), HTTPClient: config.HTTPClient}, openai.DialectConfig{Provider: Provider, TokenLimitField: openai.TokenLimitMaxTokens})
	if err != nil {
		return nil, fmt.Errorf("ollama: construct OpenAI-compatible chat: %w", err)
	}
	return corechat.NewStreamModel(protocol)
}

func resolveOpenAIBaseURL(base string) string {
	base = strings.TrimRight(cmp.Or(base, DefaultBaseURL), "/")
	if strings.HasSuffix(base, OpenAICompatPath) {
		return base
	}
	return base + OpenAICompatPath
}
