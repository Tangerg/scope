package chat

import (
	"context"
	"iter"
)

// Model performs a complete provider-neutral exchange; streaming is a separate capability.
type Model interface {
	// Call performs one complete model exchange. It must reject an invalid
	// request before provider I/O, must not retain or mutate request, and
	// transfers ownership of the returned response to the caller. Context
	// cancellation remains identifiable through errors.Is. A valid response may
	// accompany a postprocessing error: it retains known generation and usage,
	// without establishing success of the complete invocation. ClassifyFailure
	// reads failure evidence scoped to this invocation, independently of Cause.
	Call(ctx context.Context, request *Request) (*Response, error)
}

type ModelFunc func(ctx context.Context, request *Request) (*Response, error)

func (m ModelFunc) Call(ctx context.Context, request *Request) (*Response, error) {
	return m(ctx, request)
}

// Streamer is the optional streaming capability; [ResponseAccumulator] owns
// aggregation of its deltas.
type Streamer interface {
	// Stream starts provider work lazily when the sequence is iterated. Each
	// yielded delta is independently owned, carries a cumulative usage snapshot,
	// and is accepted by ResponseAccumulator. A failure yields (nil, err) at most
	// once, preserving context error identity. Stopping iteration releases
	// provider resources before the iterator returns, without a cancellation
	// yield or detached goroutine. A terminal generation may precede a failed
	// middleware operation; its response evidence survives while ClassifyFailure
	// describes the complete invocation.
	Stream(ctx context.Context, request *Request) iter.Seq2[*ResponseDelta, error]
}

type StreamerFunc func(ctx context.Context, request *Request) iter.Seq2[*ResponseDelta, error]

func (s StreamerFunc) Stream(ctx context.Context, request *Request) iter.Seq2[*ResponseDelta, error] {
	return s(ctx, request)
}
