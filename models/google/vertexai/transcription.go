package vertexai

import (
	"context"

	"github.com/Tangerg/scope/core/transcription"
	"github.com/Tangerg/scope/models/google/internal/protocol"
)

// TranscriptionModelConfig binds provider access and defaults shared by every transcription call.
type TranscriptionModelConfig struct {
	Client         ClientConfig
	DefaultOptions transcription.Options
}

func (t TranscriptionModelConfig) Validate() error {
	return t.protocol().Validate()
}

func (t TranscriptionModelConfig) protocol() protocol.TranscriptionModelConfig {
	return protocol.TranscriptionModelConfig{
		Provider:       protocolProvider,
		Client:         t.Client.protocol(),
		DefaultOptions: t.DefaultOptions,
	}
}

var _ transcription.Model = (*TranscriptionModel)(nil)

// TranscriptionModel is the shared protocol type itself rather than a
// wrapper, so this provider adds no second public surface for callers to
// choose between.
type TranscriptionModel = callModel[transcription.Request, transcription.Response]

// NewTranscriptionModel rejects an invalid provider binding before the first transcription call.
func NewTranscriptionModel(ctx context.Context, config TranscriptionModelConfig) (*TranscriptionModel, error) {
	return newCallModel[transcription.Request, transcription.Response](protocol.NewTranscriptionModel(ctx, config.protocol()))
}
