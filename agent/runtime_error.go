package agent

import (
	"fmt"
	"slices"
)

// RuntimeError reports that the active writer stopped without establishing the
// requested Process result or subtree completion. A result acknowledged before
// a descendant failed remains available through Process.Await. The Host may
// reconcile storage and restore its authoritative tree head in another Engine.
// This error does not terminate the durable execution or authorize replay of an
// uncertain Effect.
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

// Unwrap preserves the durability failure, including ownership and content
// conflicts, for errors.Is and errors.As.
func (r *RuntimeError) Unwrap() error { return r.cause }

// ProcessID identifies the affected Process handle in the stopped instance.
func (r *RuntimeError) ProcessID() ProcessID { return r.processID }

// IncarnationID identifies the durable writer that stopped.
func (r *RuntimeError) IncarnationID() TreeIncarnationID { return r.incarnationID }

// HeadDigest identifies the last tree head acknowledged to this instance. The
// store may have advanced further if a commit response was lost or a new writer
// acquired ownership; recovery must read the store's authoritative head.
func (r *RuntimeError) HeadDigest() Digest { return r.headDigest }

// UnresolvedEffectIDs returns the sorted, distinct identities whose external
// outcomes this instance could not establish durably. Pending Effects that
// were never dispatched by this instance are not added solely for a failed
// pending-boundary acknowledgment.
func (r *RuntimeError) UnresolvedEffectIDs() []EffectID {
	return slices.Clone(r.unresolvedEffectIDs)
}
