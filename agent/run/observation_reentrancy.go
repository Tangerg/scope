package run

import (
	"errors"
)

// ErrListenerReentrancy reports an operation that would wait for the active
// listener carrying its context, or would issue a same-tree operation from an
// active event callback. Derived contexts retain this restriction
// until the callback returns. Replacing the context does not remove the
// listener's obligation to avoid waiting for itself.
var ErrListenerReentrancy = errors.New("agent: operation would wait for its active listener")

// The Runtime's observation bus distinguishes independent restorations of the
// same root. Different keys also preserve active ancestors through nested
// callbacks without retaining a separate call-chain representation.
type activeEventListenerKey struct {
	bus    *observationBus
	rootID ProcessID
}

type activeJoinKey struct {
	bus *observationBus
}
