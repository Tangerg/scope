package azureopenai

import (
	"context"

	"github.com/Tangerg/scope/core/image"
	"github.com/Tangerg/scope/models/protocol/openai"
)

type ImageModelConfig struct {
	Config
	DefaultOptions image.Options
}

func (i ImageModelConfig) resolve() (endpointConfig, error) {
	return i.resolveModel(i.DefaultOptions.Model, i.DefaultOptions.Validate)
}

func (i ImageModelConfig) Validate() error {
	_, err := i.resolve()
	return err
}

func NewImageModel(ctx context.Context, config ImageModelConfig) (*openai.ImageModel, error) {
	endpoint, err := config.resolve()
	if err != nil {
		return nil, err
	}
	return openai.NewImageModel(ctx, openai.ImageModelConfig{
		Provider:       protocolProvider,
		APIKey:         endpoint.apiKey,
		DefaultOptions: config.DefaultOptions,
		BaseURL:        endpoint.baseURL,
		HTTPClient:     endpoint.httpClient,
	})
}
