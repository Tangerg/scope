package interaction

import (
	"context"
	"fmt"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/chat"
)

// ContextPolicy rewrites the retained context before each model call from the
// request that call would send, Tool definitions included, and returns the
// replacement messages. It runs inside the Step and is replayed on recovery,
// so it must be deterministic and bounded, perform no I/O, and neither mutate
// nor retain request. Revision covers it. Use ContextPreparer for retrieval or
// model-based summaries.
type ContextPolicy func(request *chat.Request) ([]chat.Message, error)

// ContextInvocation identifies preparation for a model call. Its Effect is the
// preparation's own logical operation, not the subsequent model operation; its
// Epoch fences the external writes of a superseded writer.
type ContextInvocation struct {
	Effect   run.EffectRequest
	Sequence uint64
}

// ContextPreparer rewrites the retained context through external work, such as
// a model summary, as its own recorded Effect before a model call.
type ContextPreparer interface {
	// PreparationTrigger returns the predicate deciding, before each model
	// call, whether PrepareContext runs. It receives the call's sequence and
	// the request the call would send. Compile reads it once; the predicate
	// then runs inside the Step and is replayed on recovery, so it must be
	// deterministic and bounded, neither mutate nor retain its arguments, and
	// capture neither the preparer nor any backend. Nil is a configuration
	// error. Revision covers it.
	PreparationTrigger() func(sequence uint64, request *chat.Request) bool

	// PrepareContext returns the complete replacement message sequence. It
	// runs only when the trigger holds, as an Effect recorded once. An error
	// leaves the Effect Unknown unless it is a ContextFailure proving a
	// definite outcome.
	PrepareContext(ctx context.Context, invocation ContextInvocation, request *chat.Request) ([]chat.Message, error)
}

// preparationTrigger reads the trigger of preparer once, so the Step side
// keeps a frozen predicate and never the preparer itself.
func preparationTrigger(preparer ContextPreparer) (trigger func(uint64, *chat.Request) bool, err error) {
	if preparer == nil {
		return nil, nil
	}
	if lo.IsNil(preparer) {
		return nil, fmt.Errorf("%w: ContextPreparer is typed nil", ErrInvalidConfig)
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			trigger, err = nil, fmt.Errorf("%w: %w", ErrInvalidConfig, &run.CallbackPanicError{Operation: "ContextPreparer.PreparationTrigger", Value: recovered})
		}
	}()
	if trigger = preparer.PreparationTrigger(); trigger == nil {
		return nil, fmt.Errorf("%w: ContextPreparer.PreparationTrigger returned nil", ErrInvalidConfig)
	}
	return trigger, nil
}

// ContextFailure reports a definite rejection by a context preparer; an
// ordinary transport error cannot establish this guarantee.
type ContextFailure struct{ Cause error }

func (c *ContextFailure) Error() string {
	return fmt.Sprintf("interaction: context preparation failed: %v", c.Cause)
}

func (c *ContextFailure) Unwrap() error { return c.Cause }
