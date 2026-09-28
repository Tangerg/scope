package speech

import (
	"context"
	"iter"
)

// Model is the synchronous provider-neutral speech generation SPI. Call
// implementations validate requests before I/O, reject explicit options they
// cannot represent, preserve context error identity, and return responses that
// pass Validate.
type Model interface {
	// Call produces one complete audio response from a validated request. It
	// must not retain or mutate request, transfers response ownership to the
	// caller, and preserves context cancellation for errors.Is.
	Call(ctx context.Context, request *Request) (*Response, error)
}

type ModelFunc func(context.Context, *Request) (*Response, error)

func (m ModelFunc) Call(ctx context.Context, request *Request) (*Response, error) {
	return m(ctx, request)
}

// Streamer is the optional streaming capability. Every yielded response obeys
// the [Model] response contract. It is independent from [Model], so callers only
// require streaming when they consume it.
type Streamer interface {
	// Stream begins synthesis lazily when iterated and yields independently owned
	// audio chunks in provider order. Stopping iteration releases provider
	// resources synchronously; a terminal error is yielded at most once.
	Stream(ctx context.Context, request *Request) iter.Seq2[*Response, error]
}

type StreamerFunc func(context.Context, *Request) iter.Seq2[*Response, error]

func (s StreamerFunc) Stream(ctx context.Context, request *Request) iter.Seq2[*Response, error] {
	return s(ctx, request)
}
