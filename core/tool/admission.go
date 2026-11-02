package tool

import (
	"fmt"
	"unicode/utf8"
)

// IdempotencyKey names one logical call across its replays and continuations,
// so a backend with external effects can deduplicate them. It is opaque: a
// Tool may forward it to third parties, so it never encodes runtime structure.
// The zero value means the caller has no durable identity for the call.
type IdempotencyKey string

// Admission is what the driving runtime, never the proposer, asserts about one
// call. It is separate from Request because a model can forge arguments but
// must not be able to choose the identity external systems deduplicate on.
type Admission struct {
	IdempotencyKey IdempotencyKey
	// Continuation answers the InputRequest committed by this same logical
	// call; nil starts a new call.
	Continuation *Continuation
}

func (a Admission) validate() error {
	if !utf8.ValidString(string(a.IdempotencyKey)) {
		return fmt.Errorf("%w: idempotency key is not valid UTF-8", ErrInvalidInvocation)
	}
	if a.Continuation != nil && !a.Continuation.valid() {
		return fmt.Errorf("%w: invalid continuation", ErrInvalidInvocation)
	}
	return nil
}
