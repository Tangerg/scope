package azureopenai

import (
	"context"

	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/models/protocol/openai"
)

// EmbeddingModelConfig binds provider access and defaults shared by every embedding call.
type EmbeddingModelConfig struct {
	Config
	DefaultOptions embedding.Options
}

func (e EmbeddingModelConfig) resolve() (endpointConfig, error) {
	return resolveModelConfig(e.Config, e.DefaultOptions.Model, e.DefaultOptions.Validate)
}

func (e EmbeddingModelConfig) Validate() error {
	_, err := e.resolve()
	return err
}

var _ embedding.Model = (*EmbeddingModel)(nil)

// EmbeddingModel is the shared protocol type itself rather than a wrapper,
// so this provider adds no second public surface for callers to choose
// between.
type EmbeddingModel = openai.EmbeddingModel

// NewEmbeddingModel rejects an invalid provider binding before the first embedding call.
func NewEmbeddingModel(ctx context.Context, config EmbeddingModelConfig) (*EmbeddingModel, error) {
	endpoint, err := config.resolve()
	if err != nil {
		return nil, err
	}
	return openai.NewEmbeddingModel(ctx, openai.EmbeddingModelConfig{
		Provider:       protocolProvider,
		APIKey:         endpoint.apiKey,
		DefaultOptions: config.DefaultOptions,
		BaseURL:        endpoint.baseURL,
		HTTPClient:     endpoint.httpClient,
	})
}
