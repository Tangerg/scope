package chatclient

import (
	"context"
	"errors"
	"iter"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/invocation"
)

var ErrNilStreamer = errors.New("chatclient: nil streamer")

// StreamClient owns a required streaming capability; compose middleware with
// chat.WrapStream before NewStreamClient.
// It is immutable; concurrent use requires a concurrency-safe Streamer. It does
// not implement chat.Model or require an unused synchronous capability.
type StreamClient struct {
	streamer chat.Streamer
}

func NewStreamClient(streamer chat.Streamer) (StreamClient, error) {
	if lo.IsNil(streamer) {
		return StreamClient{}, ErrNilStreamer
	}
	return StreamClient{streamer: streamer}, nil
}

// Stream snapshots the request at invocation. Provider work remains lazy until
// iteration; stopping iteration synchronously releases the provider resources.
func (s StreamClient) Stream(ctx context.Context, request *chat.Request) iter.Seq2[*chat.ResponseDelta, error] {
	if lo.IsNil(s.streamer) {
		return errorSequence(&invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: ErrInvalidClient})
	}
	prepared, err := prepareRequest(request, nil)
	if err != nil {
		return errorSequence(&invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: err})
	}
	return func(yield func(*chat.ResponseDelta, error) bool) {
		sequence := s.streamer.Stream(ctx, prepared)
		if sequence == nil {
			yield(nil, chat.ErrNilStream)
			return
		}
		sequence(yield)
	}
}

func errorSequence(err error) iter.Seq2[*chat.ResponseDelta, error] {
	return func(yield func(*chat.ResponseDelta, error) bool) { yield(nil, err) }
}

var _ chat.Streamer = StreamClient{}
