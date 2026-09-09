package agent

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
)

// ErrListenerReentrancy reports a tree operation, Process control, or Await
// using the context of an active synchronous [EventListener] for that Engine
// and root. Derived contexts retain this restriction until the callback returns.
// A callback that replaces its context still must obey the listener contract.
var ErrListenerReentrancy = errors.New("agent: listener called its own tree")

// The Engine's observation bus distinguishes independent restorations of the
// same root. Different keys also preserve active ancestors through nested
// callbacks without retaining a separate call-chain representation.
type observedTreeKey struct {
	bus    *observationBus
	rootID ProcessID
}

func (o *observationBus) checkListenerReentrancy(ctx context.Context, rootID ProcessID, operation string) error {
	active, ok := ctx.Value(observedTreeKey{bus: o, rootID: rootID}).(*atomic.Bool)
	if !ok || !active.Load() {
		return nil
	}
	return fmt.Errorf("%w: %s on tree %s is unavailable while its listener is active", ErrListenerReentrancy, operation, rootID)
}
