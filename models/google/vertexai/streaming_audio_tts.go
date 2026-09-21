package vertexai

import (
	"context"
	"errors"
	"iter"

	tts "github.com/Tangerg/scope/core/speech"
	"github.com/Tangerg/scope/models/google/internal/protocol"
)

// StreamingAudioTTSModel exposes native incremental audio synthesis.
type StreamingAudioTTSModel protocol.StreamingAudioTTSModel

func NewStreamingAudioTTSModel(ctx context.Context, config AudioTTSModelConfig) (*StreamingAudioTTSModel, error) {
	adapter, err := protocol.NewStreamingAudioTTSModel(ctx, config.protocol())
	if err != nil {
		return nil, err
	}
	return (*StreamingAudioTTSModel)(adapter), nil
}

func (s *StreamingAudioTTSModel) Stream(ctx context.Context, req *tts.Request) iter.Seq2[*tts.Response, error] {
	if s == nil {
		return func(yield func(*tts.Response, error) bool) {
			yield(nil, errors.New("vertexai: nil StreamingAudioTTSModel"))
		}
	}
	if err := req.Validate(); err != nil {
		return func(yield func(*tts.Response, error) bool) { yield(nil, err) }
	}
	return (*protocol.StreamingAudioTTSModel)(s).Stream(ctx, req)
}
