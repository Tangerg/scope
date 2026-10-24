package chat

import (
	"context"
	"errors"
	"iter"
)

// ErrNotExecuted marks a Call or Stream error as a definite non-execution: the
// provider operation did not run and left no pending work, so its outcome is a
// known failure rather than an uncertain one. Middleware that rejects a request
// before delegating to the next model wraps its error with ErrNotExecuted. A
// consumer that must distinguish a definite failure from an unknown outcome, such
// as a managed Agent deciding whether an operation needs reconciliation, tests
// for it with errors.Is. An error that does not wrap it keeps the safe default:
// the operation may have executed and its outcome is unknown.
var ErrNotExecuted = errors.New("chat: model operation not executed")

// Model performs a complete provider-neutral exchange; streaming is a separate capability.
type Model interface {
	// Call performs one complete model exchange. It must reject an invalid
	// request before provider I/O, must not retain or mutate request, and
	// transfers ownership of the returned response to the caller. Context
	// cancellation remains identifiable through errors.Is. An error that wraps
	// [ErrNotExecuted] additionally promises the provider operation did not run.
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
	// yield or detached goroutine.
	Stream(ctx context.Context, request *Request) iter.Seq2[*ResponseDelta, error]
}

type StreamerFunc func(ctx context.Context, request *Request) iter.Seq2[*ResponseDelta, error]

func (s StreamerFunc) Stream(ctx context.Context, request *Request) iter.Seq2[*ResponseDelta, error] {
	return s(ctx, request)
}
