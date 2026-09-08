package vertexai

import (
	"context"
	"errors"
	"iter"

	tts "github.com/Tangerg/scope/core/speech"
	"github.com/Tangerg/scope/models/google/internal/protocol"
)

// AudioTTSModelConfig binds provider access and defaults shared by every speech call.
type AudioTTSModelConfig struct {
	Client         ClientConfig
	DefaultOptions tts.Options
}

func (a AudioTTSModelConfig) Validate() error {
	return a.protocol().Validate()
}

func (a AudioTTSModelConfig) protocol() protocol.AudioTTSModelConfig {
	return protocol.AudioTTSModelConfig{
		Provider:       protocolProvider,
		Client:         a.Client.protocol(),
		DefaultOptions: a.DefaultOptions,
	}
}

var (
	_ tts.Model    = (*AudioTTSModel)(nil)
	_ tts.Streamer = (*AudioTTSModel)(nil)
)

// AudioTTSModel wraps this provider's protocol implementation so the wire
// type stays unexported. Callers depend on the Core modality contract, which
// lets the protocol change without breaking this module's public surface.
type AudioTTSModel protocol.AudioTTSModel

// NewAudioTTSModel rejects an invalid provider binding before the first speech call.
func NewAudioTTSModel(ctx context.Context, config AudioTTSModelConfig) (*AudioTTSModel, error) {
	adapter, err := protocol.NewAudioTTSModel(ctx, config.protocol())
	if err != nil {
		return nil, err
	}
	return (*AudioTTSModel)(adapter), nil
}

func (a *AudioTTSModel) Call(ctx context.Context, req *tts.Request) (*tts.Response, error) {
	if a == nil {
		return nil, errors.New("vertexai: nil AudioTTSModel")
	}
	return (*protocol.AudioTTSModel)(a).Call(ctx, req)
}

func (a *AudioTTSModel) Stream(ctx context.Context, req *tts.Request) iter.Seq2[*tts.Response, error] {
	if a == nil {
		return func(yield func(*tts.Response, error) bool) { yield(nil, errors.New("vertexai: nil AudioTTSModel")) }
	}
	if err := req.Validate(); err != nil {
		return func(yield func(*tts.Response, error) bool) { yield(nil, err) }
	}
	return (*protocol.AudioTTSModel)(a).Stream(ctx, req)
}
