package azureopenai

import (
	"context"

	"github.com/Tangerg/scope/core/transcription"
	"github.com/Tangerg/scope/models/protocol/openai"
)

// TranscriptionRequestExtensionKey holds the provider-native transcription request fields Core does not
// represent; fields Core owns are rejected rather than overridden.
const TranscriptionRequestExtensionKey = Provider + openai.TranscriptionRequestExtensionSuffix

type TranscriptionModelConfig struct {
	Config
	DefaultOptions transcription.Options
}

func (t TranscriptionModelConfig) resolve() (endpointConfig, error) {
	return t.resolveModel(t.DefaultOptions.Model, t.DefaultOptions.Validate)
}

func (t TranscriptionModelConfig) Validate() error {
	_, err := t.resolve()
	return err
}

func NewTranscriptionModel(ctx context.Context, config TranscriptionModelConfig) (*openai.TranscriptionModel, error) {
	endpoint, err := config.resolve()
	if err != nil {
		return nil, err
	}
	return openai.NewTranscriptionModel(ctx, openai.TranscriptionModelConfig{
		Provider:       Provider,
		APIKey:         endpoint.apiKey,
		DefaultOptions: config.DefaultOptions,
		BaseURL:        endpoint.baseURL,
		HTTPClient:     endpoint.httpClient,
	})
}
