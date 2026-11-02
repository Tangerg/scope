package tool

import (
	"fmt"
	"unicode/utf8"
)

// Admission is what the driving runtime, never the proposer, asserts about one
// call. It is separate from Request because a model can forge arguments but
// must not be able to choose the identity external systems deduplicate on.
type Admission struct {
	// IdempotencyKey names the logical call across replays and continuations,
	// so a Tool with external effects can let its backend deduplicate them.
	// Empty means the caller has no durable identity for the call.
	IdempotencyKey string
	// Continuation answers the InputRequest committed by this same logical
	// call; nil starts a new call.
	Continuation *Continuation
}

func (a Admission) validate() error {
	if !utf8.ValidString(a.IdempotencyKey) {
		return fmt.Errorf("%w: idempotency key is not valid UTF-8", ErrInvalidInvocation)
	}
	if a.Continuation != nil && !a.Continuation.valid() {
		return fmt.Errorf("%w: invalid continuation", ErrInvalidInvocation)
	}
	return nil
}
