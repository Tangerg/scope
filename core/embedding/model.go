package embedding

import "context"

// Model is the complete provider-neutral embedding SPI. Implementations
// reject explicit options they cannot represent and return responses that pass
// Validate. Defaults, identity, batching, and dimension discovery are
// independent concerns.
// Failures use [github.com/Tangerg/scope/core/invocation.ClassifyFailure] for
// evidence about the complete call. A nested failure cannot prove that earlier
// provider work did not run, and no failure kind authorizes retry.
type Model interface {
	// Call performs one embedding request after validating the complete batch.
	// It must not retain or mutate request, returns outputs in input order, and
	// transfers ownership of the response to the caller. Context cancellation
	// remains identifiable through errors.Is.
	Call(ctx context.Context, request *Request) (*Response, error)
}

type ModelFunc func(context.Context, *Request) (*Response, error)

func (m ModelFunc) Call(ctx context.Context, request *Request) (*Response, error) {
	return m(ctx, request)
}
