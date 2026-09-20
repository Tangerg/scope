package agent

import (
	"errors"
	"fmt"
	"slices"
)

// RuntimeError reports that the active runtime stopped without establishing the
// requested Process result or subtree completion. A result acknowledged before
// a descendant failed remains available through Process.Await. The Host may
// reconcile storage and restore its authoritative tree head in another Engine.
// Await alone does not establish that descendant work has drained. This error
// does not establish logical termination or authorize replay of an uncertain Effect.
// Engine constructs these errors; the zero value carries no runtime identity.
// Process methods and tree reports return independent RuntimeError values;
// Unwrap preserves the original cause.
type RuntimeError struct {
	processID           ProcessID
	incarnationID       TreeIncarnationID
	headDigest          Digest
	unresolvedEffectIDs []EffectID
	cause               error
}

func (r *RuntimeError) clone() *RuntimeError {
	if r == nil {
		return nil
	}
	owned := *r
	return &owned
}

func (r *RuntimeError) Error() string {
	return fmt.Sprintf("agent: runtime for Process %s stopped: %v", r.processID, r.cause)
}

// Unwrap preserves the capacity or committer failure, including ownership and
// content conflicts, for errors.Is and errors.As.
func (r *RuntimeError) Unwrap() error { return r.cause }

// ProcessID identifies the affected Process handle in the stopped instance.
func (r *RuntimeError) ProcessID() ProcessID { return r.processID }

// IncarnationID identifies the writer that stopped.
func (r *RuntimeError) IncarnationID() TreeIncarnationID { return r.incarnationID }

// HeadDigest identifies the last tree head acknowledged to this instance. The
// store may have advanced further if a commit response was lost or a new writer
// acquired ownership; recovery must read the store's authoritative head.
func (r *RuntimeError) HeadDigest() Digest { return r.headDigest }

// UnresolvedEffectIDs returns the sorted, distinct identities whose external
// outcomes this instance could not adopt or establish durably. Pending Effects
// that were never dispatched by this instance are not added solely for a failed
// pending-boundary acknowledgment.
func (r *RuntimeError) UnresolvedEffectIDs() []EffectID {
	return slices.Clone(r.unresolvedEffectIDs)
}

func newTreeRuntimeFailure(cause error) Failure {
	if sealed, ok := errors.AsType[*callbackError](cause); ok && sealed != nil {
		return sealed.runtime
	}
	kind := FailureKindExternal
	code := failureCodeEngineTreeCommitterFailed
	switch {
	case errors.Is(cause, ErrResourceLimitExceeded):
		kind, code = FailureKindExecution, failureCodeEngineLimitSnapshot
	case errors.Is(cause, ErrCommitConflict):
		kind = FailureKindContract
		code = failureCodeEngineTreeCommitterConflict
	case errors.Is(cause, ErrTreeIncarnationConflict):
		code = failureCodeEngineTreeIncarnationConflict
	}
	return newEngineFailure(kind, code, cause)
}
