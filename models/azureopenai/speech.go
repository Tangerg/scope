package azureopenai

import (
	"context"

	"github.com/Tangerg/scope/core/speech"
	"github.com/Tangerg/scope/models/protocol/openai"
)

// SpeechRequestExtensionKey holds the provider-native speech request fields Core does not
// represent; fields Core owns are rejected rather than overridden.
const SpeechRequestExtensionKey = Provider + openai.SpeechRequestExtensionSuffix

type SpeechModelConfig struct {
	Config
	DefaultOptions   speech.Options
	MaxResponseBytes speech.AudioByteLimit
}

func (s SpeechModelConfig) resolve() (endpointConfig, error) {
	endpoint, err := s.resolveModel(s.DefaultOptions.Model, s.DefaultOptions.Validate)
	if err != nil {
		return endpointConfig{}, err
	}
	return endpoint, nil
}

func (s SpeechModelConfig) Validate() error {
	_, err := s.resolve()
	return err
}

func NewSpeechModel(ctx context.Context, config SpeechModelConfig) (*openai.SpeechModel, error) {
	endpoint, err := config.resolve()
	if err != nil {
		return nil, err
	}
	return openai.NewSpeechModel(ctx, openai.SpeechModelConfig{
		Provider:         Provider,
		APIKey:           endpoint.apiKey,
		DefaultOptions:   config.DefaultOptions,
		BaseURL:          endpoint.baseURL,
		HTTPClient:       endpoint.httpClient,
		MaxResponseBytes: config.MaxResponseBytes,
	})
}
