package chat

import (
	"context"
	"errors"
	"iter"

	"github.com/samber/lo"
)

var errMissingStreamer = errors.New("chat: stream model requires a streamer")

// StreamModel derives complete exchanges from a true Streamer. It intentionally
// does not advertise discovery or token counting just because the underlying
// wire implementation can attempt those requests against some other endpoint.
type StreamModel struct{ source Streamer }

func NewStreamModel(source Streamer) (*StreamModel, error) {
	if lo.IsNil(source) {
		return nil, errMissingStreamer
	}
	return &StreamModel{source: source}, nil
}

func (s *StreamModel) Stream(ctx context.Context, request *Request) iter.Seq2[*ResponseDelta, error] {
	if s == nil || lo.IsNil(s.source) {
		return func(yield func(*ResponseDelta, error) bool) {
			yield(nil, &CallFailure{Kind: CallFailureNotExecuted, Cause: errMissingStreamer})
		}
	}
	return s.source.Stream(ctx, request)
}

func (s *StreamModel) Call(ctx context.Context, request *Request) (*Response, error) {
	return Collect(s.Stream(ctx, request))
}
