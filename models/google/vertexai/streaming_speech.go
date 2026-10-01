package vertexai

import (
	"context"
	"errors"
	"iter"

	"github.com/Tangerg/scope/core/speech"
	"github.com/Tangerg/scope/models/google/internal/protocol"
)

// StreamingSpeechModel exposes native incremental audio synthesis.
type StreamingSpeechModel protocol.StreamingSpeechModel

func NewStreamingSpeechModel(ctx context.Context, config SpeechModelConfig) (*StreamingSpeechModel, error) {
	adapter, err := protocol.NewStreamingSpeechModel(ctx, config.protocol())
	if err != nil {
		return nil, err
	}
	return (*StreamingSpeechModel)(adapter), nil
}

func (s *StreamingSpeechModel) Stream(ctx context.Context, req *speech.Request) iter.Seq2[*speech.Response, error] {
	if s == nil {
		return func(yield func(*speech.Response, error) bool) {
			yield(nil, errors.New("vertexai: nil StreamingSpeechModel"))
		}
	}
	if err := req.Validate(); err != nil {
		return func(yield func(*speech.Response, error) bool) { yield(nil, err) }
	}
	return (*protocol.StreamingSpeechModel)(s).Stream(ctx, req)
}

func (s *StreamingSpeechModel) Call(ctx context.Context, req *speech.Request) (*speech.Response, error) {
	if s == nil {
		return nil, errors.New("vertexai: nil StreamingSpeechModel")
	}
	return (*protocol.StreamingSpeechModel)(s).Call(ctx, req)
}
