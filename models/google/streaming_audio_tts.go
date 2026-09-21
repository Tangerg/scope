package google

import (
	"context"
	"errors"
	"iter"

	tts "github.com/Tangerg/scope/core/speech"
	"github.com/Tangerg/scope/models/google/internal/protocol"
)

// StreamingAudioTTSModel exposes native incremental audio synthesis.
type StreamingAudioTTSModel struct {
	protocol *protocol.StreamingAudioTTSModel
}

func NewStreamingAudioTTSModel(ctx context.Context, config AudioTTSModelConfig) (*StreamingAudioTTSModel, error) {
	adapter, err := protocol.NewStreamingAudioTTSModel(ctx, config.protocol())
	if err != nil {
		return nil, err
	}
	return &StreamingAudioTTSModel{protocol: adapter}, nil
}

func (s *StreamingAudioTTSModel) Stream(ctx context.Context, req *tts.Request) iter.Seq2[*tts.Response, error] {
	if s == nil || s.protocol == nil {
		return func(yield func(*tts.Response, error) bool) {
			yield(nil, errors.New("google: nil StreamingAudioTTSModel"))
		}
	}
	if err := req.Validate(); err != nil {
		return func(yield func(*tts.Response, error) bool) { yield(nil, err) }
	}
	return s.protocol.Stream(ctx, req)
}
