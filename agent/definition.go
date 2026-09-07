package agent

import "context"

// Definition is an immutable Agent behavior definition. Its methods may be
// called concurrently for different Processes. Implementations create
// a fresh Execution from validated Input or restore one from their own opaque
// ExecutionState. Definition methods must not depend on Host product
// identities, storage protocols, or mutable global registration.
type Definition interface {
	// Descriptor returns the immutable, portable contract shared by every
	// Execution created from this definition. Repeated and concurrent calls must
	// return an equivalent value; runtime configuration and mutable state do not
	// belong in the descriptor.
	Descriptor() Descriptor
	// Start validates input against Descriptor and creates a fresh, isolated
	// Execution without performing external I/O. The returned Execution has not
	// executed a Step and must not share mutable state with another Process.
	Start(input Input) (Execution, error)
	// Restore reconstructs one Execution from a state previously produced by
	// Snapshot for this exact definition. It must reject malformed state and
	// state belonging to another contract; restoration must
	// not replay external work.
	Restore(state ExecutionState) (Execution, error)
}

// Execution is the single Strategy-owned state machine inside one Process.
// Step must be a bounded, deterministic state reduction over the current state
// and the supplied Signal prefix. It must not perform external I/O, read clock,
// random or global state, or start ownerless goroutines. External operations are
// returned as Effects. Snapshot must fail rather than return partial state.
//
// The Engine is the sole caller and never invokes Step concurrently for the same
// Execution. If Step or Snapshot fails, the instance is discarded and may only
// be rebuilt from the committed ExecutionState.
type Execution interface {
	// Step reduces the current private state and the supplied ordered Signal
	// prefix into one candidate Transition. It must honor ctx for bounded CPU
	// work, perform no I/O, consume no hidden input, and never retain signals.
	// The Engine serializes calls for one Execution.
	Step(ctx context.Context, signals []Signal) (Transition, error)
	// Snapshot returns a complete, independently owned state from
	// which Definition.Restore can reproduce the current Execution exactly. It
	// must fail rather than omit state required for deterministic continuation.
	Snapshot() (ExecutionState, error)
}
