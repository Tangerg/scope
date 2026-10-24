package chat

import (
	"context"
	"errors"
	"fmt"
	"iter"
)

// ErrDefiniteFailure marks a Call or Stream error as a known, terminal failure
// rather than an uncertain outcome, so a consumer settles it as a definite failure
// instead of reconciling an unknown. Only a boundary that holds the execution
// evidence wraps it; an unmarked error keeps the safe default that the operation
// may have executed and its outcome is unknown.
var ErrDefiniteFailure = errors.New("chat: definite model failure")

// ErrNotExecuted is the definite failure in which the provider operation did not
// run. Middleware that rejects a request before delegating wraps its error with
// it; a failure after the model ran, such as a rejected output, wraps
// ErrDefiniteFailure without claiming non-execution.
var ErrNotExecuted = fmt.Errorf("%w: operation not executed", ErrDefiniteFailure)

// Model performs a complete provider-neutral exchange; streaming is a separate capability.
type Model interface {
	// Call performs one complete model exchange. It must reject an invalid
	// request before provider I/O, must not retain or mutate request, and
	// transfers ownership of the returned response to the caller. Context
	// cancellation remains identifiable through errors.Is.
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
