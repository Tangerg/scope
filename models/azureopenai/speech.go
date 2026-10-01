package azureopenai

import (
	"context"
	"errors"

	"github.com/Tangerg/scope/core/speech"
	"github.com/Tangerg/scope/models/protocol/openai"
)

// SpeechModelConfig binds provider access and defaults shared by every speech call.
type SpeechModelConfig struct {
	Config
	DefaultOptions   speech.Options
	MaxResponseBytes int64
}

func (s SpeechModelConfig) resolve() (endpointConfig, error) {
	endpoint, err := s.resolveModel(s.DefaultOptions.Model, s.DefaultOptions.Validate)
	if err != nil {
		return endpointConfig{}, err
	}
	if s.MaxResponseBytes < 0 {
		return endpointConfig{}, errors.New("azureopenai: MaxResponseBytes must not be negative")
	}
	return endpoint, nil
}

func (s SpeechModelConfig) Validate() error {
	_, err := s.resolve()
	return err
}

// NewSpeechModel rejects an invalid provider binding before the first speech call.
func NewSpeechModel(ctx context.Context, config SpeechModelConfig) (*openai.SpeechModel, error) {
	endpoint, err := config.resolve()
	if err != nil {
		return nil, err
	}
	return openai.NewSpeechModel(ctx, openai.SpeechModelConfig{
		Provider:         protocolProvider,
		APIKey:           endpoint.apiKey,
		DefaultOptions:   config.DefaultOptions,
		BaseURL:          endpoint.baseURL,
		HTTPClient:       endpoint.httpClient,
		MaxResponseBytes: config.MaxResponseBytes,
	})
}
