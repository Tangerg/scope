package vertexai

import (
	"context"

	"github.com/Tangerg/scope/core/transcription"
	"github.com/Tangerg/scope/models/google/internal/protocol"
)

// AudioTranscriptionModelConfig binds provider access and defaults shared by every transcription call.
type AudioTranscriptionModelConfig struct {
	Client         ClientConfig
	DefaultOptions transcription.Options
}

func (a AudioTranscriptionModelConfig) Validate() error {
	return a.protocol().Validate()
}

func (a AudioTranscriptionModelConfig) protocol() protocol.AudioTranscriptionModelConfig {
	return protocol.AudioTranscriptionModelConfig{
		Provider:       protocolProvider,
		Client:         a.Client.protocol(),
		DefaultOptions: a.DefaultOptions,
	}
}

var _ transcription.Model = (*AudioTranscriptionModel)(nil)

// AudioTranscriptionModel is the shared protocol type itself rather than a
// wrapper, so this provider adds no second public surface for callers to
// choose between.
type AudioTranscriptionModel = callModel[transcription.Request, transcription.Response]

// NewAudioTranscriptionModel rejects an invalid provider binding before the first transcription call.
func NewAudioTranscriptionModel(ctx context.Context, config AudioTranscriptionModelConfig) (*AudioTranscriptionModel, error) {
	return newCallModel[transcription.Request, transcription.Response](protocol.NewAudioTranscriptionModel(ctx, config.protocol()))
}
