package tool

import "context"

// Caller executes one admitted invocation. A completed result is not replaced
// by cancellation observed after execution; ordinary errors prove no outcome.
type Caller interface {
	Call(context.Context, Invocation) (Result, error)
}

type CallerFunc func(context.Context, Invocation) (Result, error)

func (c CallerFunc) Call(ctx context.Context, invocation Invocation) (Result, error) {
	return c(ctx, invocation)
}
