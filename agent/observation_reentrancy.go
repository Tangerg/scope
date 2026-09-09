package agent

import (
	"context"
	"errors"
	"fmt"
)

// ErrListenerReentrancy reports a query or control call made from inside a
// synchronous [EventListener] callback against the tree that callback is
// observing.
//
// The callback runs on that tree's own owner, and every query and control path
// waits for a turn from the same owner, so the call would wait for work the
// owner cannot start until the callback returns. Nothing breaks the wait: event
// publication deliberately hands the listener a context stripped of
// cancellation so publication order survives a canceled caller, which is
// exactly the context whose Done channel those paths would otherwise fall back
// on. Without this the violation is a permanent hang with no diagnostic.
//
// [EventListener] already states the obligation. This turns breaking it into an
// error the listener can act on, for the ordinary case of a listener that
// forwards the context it was handed.
var ErrListenerReentrancy = errors.New("agent: listener called its own tree")

// observedTreeKey carries the root of the tree whose owner is currently inside
// a listener callback. Only that tree is refused: another tree has its own
// owner and is not blocked by this callback.
type observedTreeKey struct{}

// withObservedTree marks the context handed to a synchronous listener.
func withObservedTree(ctx context.Context, rootID ProcessID) context.Context {
	if !rootID.Valid() {
		return ctx
	}
	return context.WithValue(ctx, observedTreeKey{}, rootID)
}

// checkListenerReentrancy refuses a call whose context says its own tree's
// owner is busy delivering an event to a listener.
func checkListenerReentrancy(ctx context.Context, rootID ProcessID, operation string) error {
	observed, ok := ctx.Value(observedTreeKey{}).(ProcessID)
	if !ok || observed != rootID {
		return nil
	}
	return fmt.Errorf("%w: %s on tree %s would wait for the owner delivering the event", ErrListenerReentrancy, operation, rootID)
}
