package transcription

import "context"

// Model is the complete provider-neutral transcription SPI. Call
// implementations validate requests before I/O, reject explicit options they
// cannot represent, preserve context error identity, and return responses that
// pass Validate. Provider defaults and identity belong to provider construction
// and observability.
type Model interface {
	// Call transcribes one validated media request without retaining or mutating
	// it. The returned provider-neutral response belongs to the caller, and
	// context cancellation remains identifiable through errors.Is.
	Call(ctx context.Context, request *Request) (*Response, error)
}

type ModelFunc func(context.Context, *Request) (*Response, error)

func (m ModelFunc) Call(ctx context.Context, request *Request) (*Response, error) {
	return m(ctx, request)
}
