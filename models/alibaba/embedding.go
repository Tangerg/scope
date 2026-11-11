package alibaba

import (
	"cmp"
	"context"
	"fmt"
	"net/http"

	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/models/protocol/openai"
)

// EmbeddingRequestExtensionKey holds the provider-native embedding request fields Core does not
// represent; fields Core owns are rejected rather than overridden.
const EmbeddingRequestExtensionKey = Provider + openai.EmbeddingRequestExtensionSuffix

type EmbeddingModelConfig struct {
	APIKey         string
	DefaultOptions embedding.Options
	BaseURL        string
	HTTPClient     *http.Client
}

func (e EmbeddingModelConfig) Validate() error {
	if err := e.protocol().Validate(); err != nil {
		return fmt.Errorf("alibaba: %w", err)
	}
	return nil
}

func (e EmbeddingModelConfig) protocol() openai.EmbeddingModelConfig {
	return openai.EmbeddingModelConfig{
		Provider:       Provider,
		APIKey:         e.APIKey,
		DefaultOptions: e.DefaultOptions,
		BaseURL:        cmp.Or(e.BaseURL, BaseURLChina),
		HTTPClient:     e.HTTPClient,
	}
}

func NewEmbeddingModel(ctx context.Context, config EmbeddingModelConfig) (*openai.EmbeddingModel, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	return openai.NewEmbeddingModel(ctx, config.protocol())
}
