// Package awaited owns the protocol of a Step that awaits one dispatcher
// Effect: the Turn delivers exactly that Effect's definite settlement.
package awaited

import (
	"fmt"

	"github.com/Tangerg/scope/agent/run"
)

// Settlement reads the settlement of effect from messages, which must be
// exactly its Completion. A failed settlement returns its Failure; a
// successful one decodes its payload as T. Every protocol violation wraps
// invalidProtocol.
func Settlement[T any](messages []run.Message, effect run.EffectID, invalidProtocol error) (T, *run.Failure, error) {
	var zero T
	if len(messages) != 1 {
		return zero, nil, fmt.Errorf("%w: exactly the awaited Completion is required", invalidProtocol)
	}
	completion, ok := messages[0].(run.Completion)
	if !ok || completion.Effect != effect {
		return zero, nil, fmt.Errorf("%w: message does not settle the awaited Effect", invalidProtocol)
	}
	if failure, failed := completion.Outcome.Failure(); failed {
		return zero, &failure, nil
	}
	value, err := completion.Outcome.Payload().Decode[T]()
	if err != nil {
		return zero, nil, fmt.Errorf("%w: decode settlement: %w", invalidProtocol, err)
	}
	return value, nil, nil
}
