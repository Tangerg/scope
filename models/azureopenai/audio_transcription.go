package azureopenai

import (
	"context"

	"github.com/Tangerg/scope/core/transcription"
	"github.com/Tangerg/scope/models/protocol/openai"
)

// AudioTranscriptionModelConfig binds provider access and defaults shared by every transcription call.
type AudioTranscriptionModelConfig struct {
	Config
	DefaultOptions transcription.Options
}

func (a AudioTranscriptionModelConfig) resolve() (endpointConfig, error) {
	return a.resolveModel(a.DefaultOptions.Model, a.DefaultOptions.Validate)
}

func (a AudioTranscriptionModelConfig) Validate() error {
	_, err := a.resolve()
	return err
}

// NewAudioTranscriptionModel rejects an invalid provider binding before the first transcription call.
func NewAudioTranscriptionModel(ctx context.Context, config AudioTranscriptionModelConfig) (*openai.AudioTranscriptionModel, error) {
	endpoint, err := config.resolve()
	if err != nil {
		return nil, err
	}
	return openai.NewAudioTranscriptionModel(ctx, openai.AudioTranscriptionModelConfig{
		Provider:       protocolProvider,
		APIKey:         endpoint.apiKey,
		DefaultOptions: config.DefaultOptions,
		BaseURL:        endpoint.baseURL,
		HTTPClient:     endpoint.httpClient,
	})
}
