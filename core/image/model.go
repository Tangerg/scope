package image

import "context"

// Model is the complete provider-neutral image generation SPI. Implementations
// reject explicit options they cannot represent and return responses that pass
// Validate; provider defaults and identity belong to provider construction.
type Model interface {
	// Call performs one image-generation request after validating all prompt and
	// option invariants. It must not retain or mutate request and transfers
	// ownership of the provider-neutral response to the caller. Context
	// cancellation remains identifiable through errors.Is.
	Call(ctx context.Context, request *Request) (*Response, error)
}

type ModelFunc func(context.Context, *Request) (*Response, error)

func (m ModelFunc) Call(ctx context.Context, request *Request) (*Response, error) {
	return m(ctx, request)
}
