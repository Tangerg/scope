package interaction

import (
	"context"
	"fmt"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/chat"
)

// ContextPolicy is a deterministic, bounded local transformation. It must not
// perform I/O or retain input. Use ContextProcessor for retrieval or model-based summaries.
type ContextPolicy func(ctx context.Context, input Input) ([]chat.Message, error)

// ContextInvocation identifies preparation for a model call. Its Effect is the
// preparation's own logical operation, not the subsequent model operation.
type ContextInvocation struct {
	Effect   run.EffectRequest
	Sequence uint64
}

// ContextProcessor prepares the complete message sequence as a separately
// committed external effect. An error leaves the effect Unknown unless it is
// a ContextFailure proving a definite outcome.
type ContextProcessor interface {
	PrepareContext(ctx context.Context, invocation ContextInvocation, request *chat.Request) ([]chat.Message, error)
}

// ContextFailure reports a definite rejection by a context processor; an
// ordinary transport error cannot establish this guarantee.
type ContextFailure struct{ Cause error }

func (c *ContextFailure) Error() string {
	return fmt.Sprintf("interaction: context preparation failed: %v", c.Cause)
}

func (c *ContextFailure) Unwrap() error { return c.Cause }
