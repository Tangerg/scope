package chatclient

import (
	"context"
	"errors"
	"iter"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/chat"
)

// ErrNilStreamer rejects a missing streaming dependency at construction.
var ErrNilStreamer = errors.New("chatclient: nil streamer")

var errNilStreamSequence = errors.New("chatclient: streamer returned a nil sequence")

// StreamConfig supplies streaming middleware. The first entry is the outermost
// wrapper. NewStreamClient consumes the slice without retaining it.
type StreamConfig struct {
	Middleware []chat.StreamMiddleware
}

// StreamClient owns a required streaming capability and its middleware chain.
// It is immutable; concurrent use requires a concurrency-safe Streamer. It does
// not implement chat.Model or require an unused synchronous capability.
type StreamClient struct {
	streamer chat.Streamer
}

// NewStreamClient rejects absent streaming dependencies before any work starts.
func NewStreamClient(streamer chat.Streamer, config StreamConfig) (StreamClient, error) {
	if lo.IsNil(streamer) {
		return StreamClient{}, ErrNilStreamer
	}
	streamer = chat.WrapStream(streamer, config.Middleware...)
	if lo.IsNil(streamer) {
		return StreamClient{}, errors.New("chatclient: stream middleware returned a nil streamer")
	}
	return StreamClient{streamer: streamer}, nil
}

// Stream snapshots the request at invocation. Provider work remains lazy until
// iteration; stopping iteration synchronously releases the provider resources.
func (s StreamClient) Stream(ctx context.Context, request *chat.Request) iter.Seq2[*chat.ResponseDelta, error] {
	if lo.IsNil(s.streamer) {
		return errorSequence(ErrNilStreamer)
	}
	prepared, err := prepareRequest(request, nil)
	if err != nil {
		return errorSequence(err)
	}
	return func(yield func(*chat.ResponseDelta, error) bool) {
		sequence := s.streamer.Stream(ctx, prepared)
		if sequence == nil {
			yield(nil, errNilStreamSequence)
			return
		}
		sequence(yield)
	}
}

func errorSequence(err error) iter.Seq2[*chat.ResponseDelta, error] {
	return func(yield func(*chat.ResponseDelta, error) bool) { yield(nil, err) }
}

var _ chat.Streamer = StreamClient{}
