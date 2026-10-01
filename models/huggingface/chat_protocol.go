package huggingface

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/models/protocol/openai"
)

// Namespacing preserves provider-specific data without promoting it into the
// shared Core protocol or colliding with another provider.
const (
	OpenAIRequestExtensionKey     = "huggingface/openai_request"
	OpenAIStreamChunkExtensionKey = "huggingface/openai_stream_chunk"
)

// ChatCompletionsConfig binds provider access and defaults shared by every chat call.
type ChatCompletionsConfig struct {
	APIKey         string
	DefaultOptions corechat.Options
	BaseURL        string
	HTTPClient     *http.Client
}

func (c ChatCompletionsConfig) Validate() error {
	if c.APIKey == "" {
		return errors.New("huggingface: APIKey is required")
	}
	if err := c.DefaultOptions.Validate(); err != nil {
		return fmt.Errorf("huggingface: DefaultOptions: %w", err)
	}
	return nil
}

// NewChatCompletions rejects an invalid provider binding before the first chat call.
func NewChatCompletions(ctx context.Context, config ChatCompletionsConfig) (*openai.ChatCompletions, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	protocol, err := openai.NewCompatibleChatCompletions(ctx, openai.ChatCompletionsConfig{APIKey: config.APIKey, DefaultOptions: config.DefaultOptions, BaseURL: cmp.Or(config.BaseURL, DefaultBaseURL), HTTPClient: config.HTTPClient}, openai.Dialect{Provider: "huggingface", TokenLimitField: openai.TokenLimitMaxTokens})
	if err != nil {
		return nil, fmt.Errorf("huggingface: construct OpenAI-compatible chat: %w", err)
	}
	return protocol, nil
}
