package agent

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
)

// ErrListenerReentrancy reports an operation that would wait for the active
// listener carrying its context: an EventListener's tree owner or a
// DeltaListener's delivery worker. Derived contexts retain this restriction
// until the callback returns. Replacing the context does not remove the
// listener's obligation to avoid waiting for itself.
var ErrListenerReentrancy = errors.New("agent: operation would wait for its active listener")

// The Engine's observation bus distinguishes independent restorations of the
// same root. Different keys also preserve active ancestors through nested
// callbacks without retaining a separate call-chain representation.
type observedTreeKey struct {
	bus    *observationBus
	rootID ProcessID
}

type observedDeltaKey struct {
	bus *observationBus
}

func (o *observationBus) checkDeltaListenerReentrancy(ctx context.Context, operation string) error {
	active, ok := ctx.Value(observedDeltaKey{bus: o}).(*atomic.Bool)
	if !ok || !active.Load() {
		return nil
	}
	return fmt.Errorf("%w: %s would wait for its active Delta listener", ErrListenerReentrancy, operation)
}

func (o *observationBus) checkListenerReentrancy(ctx context.Context, rootID ProcessID, operation string) error {
	active, ok := ctx.Value(observedTreeKey{bus: o, rootID: rootID}).(*atomic.Bool)
	if !ok || !active.Load() {
		return nil
	}
	return fmt.Errorf("%w: %s on tree %s is unavailable while its listener is active", ErrListenerReentrancy, operation, rootID)
}
