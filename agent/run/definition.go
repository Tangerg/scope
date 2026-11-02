package run

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
	// ChildPrograms returns every Program this definition binds in its
	// own configuration to start as a child. The Program owns these
	// bindings: Bind folds their references into the ProgramRef,
	// so a definition bound to different children is a different Program,
	// and the Runtime starts and restores these children from the bindings
	// themselves. Only children named by runtime input are left to the
	// ProgramResolver. Repeated and concurrent calls must return the same
	// set; a leaf definition returns none.
	ChildPrograms() []Program[Payload, Payload]
	// Start validates input against Descriptor and creates a fresh, isolated
	// Execution without performing external I/O. It takes no context because it
	// only initializes local state and must return promptly.
	Start(input Payload) (Execution, error)
	// Restore reconstructs one Execution from a state previously produced by
	// Snapshot for this exact definition; the Runtime enforces the pairing through
	// the snapshot's ProgramRef. Restore validates state and strategy
	// invariants without replaying external work, and runs on every Step, so it
	// must honor ctx. The Runtime's ctx carries cancellation but no Host values,
	// which must not become unrecorded inputs.
	Restore(ctx context.Context, state ExecutionState) (Execution, error)
}

// Execution is the single Strategy-owned state machine inside one Process.
// Step must be a bounded, deterministic state reduction over the current state
// and the supplied Signal prefix. It must not perform external I/O, read clock,
// random or global state, or start ownerless goroutines. External operations are
// returned as Effects. Snapshot must fail rather than return partial state.
//
// Execution methods require the initialized instance Start or Restore returned.
// The Runtime is the sole caller, supplies a non-nil context, and never invokes
// Step concurrently for the same Execution. If Step or Snapshot fails, the
// instance is discarded and rebuilt from the committed ExecutionState. A panic
// while interpreting a returned error is an execution panic.
type Execution interface {
	// Step reduces the current private state and the supplied ordered Signal
	// prefix into one candidate Transition. It must honor ctx for bounded CPU
	// work, perform no I/O, consume no hidden input, and never retain signals.
	// An error wrapping a sentinel declared with NewClassifiedError discards
	// the candidate and records that sentinel's kind and code with the complete
	// error as diagnostic; any other error discards it with
	// execution.step.failed. Fail instead commits the candidate and consumption.
	Step(ctx context.Context, signals []Signal) (Transition, error)
	// Snapshot returns a complete, independently owned state from
	// which Definition.Restore can reproduce the current Execution exactly. It
	// must fail rather than omit state required for deterministic continuation.
	Snapshot() (ExecutionState, error)
}
