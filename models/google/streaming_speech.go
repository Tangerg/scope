package google

import (
	"context"
	"errors"
	"iter"

	"github.com/Tangerg/scope/core/speech"
	"github.com/Tangerg/scope/models/google/internal/protocol"
)

// StreamingSpeechModel exposes native incremental audio synthesis.
type StreamingSpeechModel struct {
	protocol *protocol.StreamingSpeechModel
}

func NewStreamingSpeechModel(ctx context.Context, config SpeechModelConfig) (*StreamingSpeechModel, error) {
	adapter, err := protocol.NewStreamingSpeechModel(ctx, config.protocol())
	if err != nil {
		return nil, err
	}
	return &StreamingSpeechModel{protocol: adapter}, nil
}

func (s *StreamingSpeechModel) Stream(ctx context.Context, req *speech.Request) iter.Seq2[*speech.Response, error] {
	if s == nil || s.protocol == nil {
		return func(yield func(*speech.Response, error) bool) {
			yield(nil, errors.New("google: nil StreamingSpeechModel"))
		}
	}
	if err := req.Validate(); err != nil {
		return func(yield func(*speech.Response, error) bool) { yield(nil, err) }
	}
	return s.protocol.Stream(ctx, req)
}

func (s *StreamingSpeechModel) Call(ctx context.Context, req *speech.Request) (*speech.Response, error) {
	if s == nil || s.protocol == nil {
		return nil, errors.New("google: nil StreamingSpeechModel")
	}
	return s.protocol.Call(ctx, req)
}
