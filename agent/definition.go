package agent

import "context"

// Definition is an immutable Agent behavior definition. Its methods may be
// called concurrently for different Processes. Implementations create
// a fresh Execution from validated input or restore one from their own opaque
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
	// Start must return promptly; it has no cancellation context because it only
	// initializes fresh local state. Restore may validate substantial persisted
	// state on every Step and therefore must cooperate with cancellation.
	Start(input Payload) (Execution, error)
	// Restore reconstructs one Execution from a state previously produced by
	// Snapshot for this exact definition. The caller must supply the matching
	// definition; Engine enforces this with the snapshot's exact DeploymentRef.
	// Restore validates state structure and strategy invariants without replaying
	// external work. It must honor ctx during bounded CPU work and may not use
	// context values as unrecorded inputs. Engine supplies a non-nil context with
	// cancellation but without Host values. Opaque state need not independently
	// identify its deployment.
	Restore(ctx context.Context, state ExecutionState) (Execution, error)
}

// Execution is the single Strategy-owned state machine inside one Process.
// Step must be a bounded, deterministic state reduction over the current state
// and the supplied Signal prefix. It must not perform external I/O, read clock,
// random or global state, or start ownerless goroutines. External operations are
// returned as Effects. Snapshot must fail rather than return partial state.
//
// Start and Restore own construction validity; Execution methods require the
// initialized instance they returned. Nil and zero private implementations have
// no protocol meaning.
// The Engine is the sole caller and never invokes Step concurrently for the same
// Execution. If Step or Snapshot fails, the instance is discarded and may only
// be rebuilt from the committed ExecutionState.
// The Engine always supplies a non-nil Step context. Callback containment also
// covers interpretation of returned errors: their methods may not escape into
// the scheduler. A panic during interpretation is an execution panic.
type Execution interface {
	// Step reduces the current private state and the supplied ordered Signal
	// prefix into one candidate Transition. It must honor ctx for bounded CPU
	// work, perform no I/O, consume no hidden input, and never retain signals.
	// The Engine serializes calls for one Execution. A *StepError discards the
	// candidate and preserves its Failure; an ordinary error discards it with
	// execution.step.failed. Fail instead commits the candidate and consumption.
	Step(ctx context.Context, signals []Signal) (Transition, error)
	// Snapshot returns a complete, independently owned state from
	// which Definition.Restore can reproduce the current Execution exactly. It
	// must fail rather than omit state required for deterministic continuation.
	Snapshot() (ExecutionState, error)
}
