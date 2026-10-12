package vertexai

import (
	"context"
	"errors"

	"github.com/Tangerg/scope/core/speech"
	"github.com/Tangerg/scope/models/google/internal/protocol"
)

type SpeechModelConfig struct {
	Client         ClientConfig
	DefaultOptions speech.Options
}

func (s SpeechModelConfig) Validate() error {
	return s.protocol().Validate()
}

func (s SpeechModelConfig) protocol() protocol.SpeechModelConfig {
	return protocol.SpeechModelConfig{
		Provider:       protocolProvider,
		Client:         s.Client.protocol(),
		DefaultOptions: s.DefaultOptions,
	}
}

var (
	_ speech.Model = (*SpeechModel)(nil)
)

// SpeechModel wraps this provider's protocol implementation so the wire
// type stays unexported. Callers depend on the Core modality contract, which
// lets the protocol change without breaking this module's public surface.
type SpeechModel protocol.SpeechModel

func NewSpeechModel(ctx context.Context, config SpeechModelConfig) (*SpeechModel, error) {
	adapter, err := protocol.NewSpeechModel(ctx, config.protocol())
	if err != nil {
		return nil, err
	}
	return (*SpeechModel)(adapter), nil
}

func (s *SpeechModel) Call(ctx context.Context, req *speech.Request) (*speech.Response, error) {
	if s == nil {
		return nil, errors.New("vertexai: nil SpeechModel")
	}
	return (*protocol.SpeechModel)(s).Call(ctx, req)
}
