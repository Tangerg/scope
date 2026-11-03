package rerank

import "context"

// Model is the complete provider-neutral reranking SPI. Implementations
// validate requests before I/O, reject explicit options they cannot represent,
// preserve context error identity, and return responses that pass ValidateFor.
// Failures use [github.com/Tangerg/scope/core/invocation.ClassifyFailure] for
// evidence about the complete call. A nested failure cannot prove that earlier
// provider work did not run, and no failure kind authorizes retry.
type Model interface {
	// Call ranks an immutable document batch for one query. It must not retain or
	// mutate request and transfers ownership of the response to the caller.
	Call(ctx context.Context, request *Request) (*Response, error)
}

type ModelFunc func(context.Context, *Request) (*Response, error)

func (m ModelFunc) Call(ctx context.Context, request *Request) (*Response, error) {
	return m(ctx, request)
}
