package azureopenai

import (
	"context"

	"github.com/Tangerg/scope/core/transcription"
	"github.com/Tangerg/scope/models/protocol/openai"
)

// TranscriptionModelConfig binds provider access and defaults shared by every transcription call.
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

// NewTranscriptionModel rejects an invalid provider binding before the first transcription call.
func NewTranscriptionModel(ctx context.Context, config TranscriptionModelConfig) (*openai.TranscriptionModel, error) {
	endpoint, err := config.resolve()
	if err != nil {
		return nil, err
	}
	return openai.NewTranscriptionModel(ctx, openai.TranscriptionModelConfig{
		Provider:       protocolProvider,
		APIKey:         endpoint.apiKey,
		DefaultOptions: config.DefaultOptions,
		BaseURL:        endpoint.baseURL,
		HTTPClient:     endpoint.httpClient,
	})
}
