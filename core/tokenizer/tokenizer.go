package tokenizer

import "context"

// TextCounter reports the token count a model would assign to text.
// Implementations may use a local vocabulary or a provider API.
type TextCounter interface {
	// CountText returns a non-negative count under the implementation's
	// explicitly selected vocabulary or provider model. Remote implementations
	// must honor cancellation and preserve context errors.
	CountText(ctx context.Context, text string) (int, error)
}

// Encoder converts text into vocabulary token IDs.
type Encoder interface {
	// Encode returns token IDs in vocabulary order. The caller owns the returned
	// slice; implementations must not expose a mutable internal buffer.
	Encode(ctx context.Context, text string) ([]int, error)
}

// Decoder converts vocabulary token IDs back into text.
type Decoder interface {
	// Decode rejects token IDs outside the selected vocabulary and does not
	// retain the caller's slice. Context cancellation remains identifiable.
	Decode(ctx context.Context, tokens []int) (string, error)
}

// Tokenizer combines the encoding capabilities required by token-aware text
// splitters. Providers that only count tokens should implement TextCounter
// instead.
type Tokenizer interface {
	Encoder
	Decoder
}
