package moderation

import "context"

// Model is the complete provider-neutral moderation SPI. Implementations
// reject explicit options they cannot represent and return responses that pass
// Validate; provider defaults and identity belong to provider construction.
type Model interface {
	// Call classifies one validated input batch without retaining or mutating the
	// request. Outputs preserve input order, the returned response belongs to the
	// caller, and context cancellation remains identifiable through errors.Is.
	Call(ctx context.Context, request *Request) (*Response, error)
}

type ModelFunc func(context.Context, *Request) (*Response, error)

func (m ModelFunc) Call(ctx context.Context, request *Request) (*Response, error) {
	return m(ctx, request)
}
