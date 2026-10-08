package agent

import (
	"context"
	"encoding/json"
)

// ReplayPolicy states whether a Dispatcher can prove that repeating an Effect
// with the same EffectID is the same logical external operation. It does not
// claim transactionality or allow replay under a different identity.
type ReplayPolicy string

const (
	ReplayPolicyInvalid ReplayPolicy = ""
	// ReplayPolicyNever forbids both restored pending replay and explicit
	// replay of an Unknown. A host can still supply a definite settlement.
	ReplayPolicyNever        ReplayPolicy = "never"
	ReplayPolicySameIdentity ReplayPolicy = "same_identity"
)

func (r ReplayPolicy) Valid() bool {
	switch r {
	case ReplayPolicyNever, ReplayPolicySameIdentity:
		return true
	default:
		return false
	}
}

func (r ReplayPolicy) String() string {
	if !r.Valid() {
		return invalidEnumName
	}
	return string(r)
}

// EffectRequest is the immutable dispatch context prepared by the Engine.
type EffectRequest struct {
	incarnationID TreeIncarnationID
	deploymentRef DeploymentRef
	relation      ProcessRelation
	stepSequence  uint64
	batchIndex    uint32
	attemptID     EffectAttemptID
	effect        Effect
}

func (e EffectRequest) clone() EffectRequest {
	e.effect = e.effect.clone()
	return e
}

func (e EffectRequest) Valid() bool {
	return e.deploymentRef.Valid() && e.relation.Valid() && e.stepSequence > 0 &&
		e.effect.Valid()
}

func newEffectRequest(
	incarnationID TreeIncarnationID,
	deploymentRef DeploymentRef,
	relation ProcessRelation,
	stepSequence uint64,
	batchIndex uint32,
	effect Effect,
) EffectRequest {
	return EffectRequest{
		incarnationID: incarnationID, deploymentRef: deploymentRef, relation: relation,
		stepSequence: stepSequence, batchIndex: batchIndex,
		effect: effect.clone(),
	}
}

// TreeIncarnationID identifies the active durable writer for observation and
// correlation. It is not part of the Effect's idempotency identity, which
// remains ID across restoration.
func (e EffectRequest) TreeIncarnationID() (TreeIncarnationID, bool) {
	return e.incarnationID, e.incarnationID.Valid()
}

func (e EffectRequest) DeploymentRef() DeploymentRef { return e.deploymentRef }

func (e EffectRequest) Relation() ProcessRelation { return e.relation }

// StepSequence returns the one-based Step sequence that declared the Effect.
func (e EffectRequest) StepSequence() uint64 { return e.stepSequence }

// BatchIndex returns the zero-based declaration order within the Step Effect batch.
func (e EffectRequest) BatchIndex() uint32 { return e.batchIndex }

// ID derives from the Process, Step, and batch position the request names.
func (e EffectRequest) ID() EffectID {
	return e.relation.ProcessID().effectID(e.stepSequence, int(e.batchIndex))
}

// AttemptID identifies this physical Dispatch invocation. Captured requests and
// durability boundaries describe logical Effects without an active invocation;
// they return false. Replay keeps ID and receives a fresh AttemptID.
func (e EffectRequest) AttemptID() (EffectAttemptID, bool) {
	return e.attemptID, e.attemptID.Valid()
}

// Effect returns an independently owned copy of the frozen intent.
func (e EffectRequest) Effect() Effect { return e.effect.clone() }

// DeltaEmitter accepts Strategy-owned streaming payloads while Dispatch is
// active. The Engine validates, orders, bounds, and publishes each payload as a
// best-effort Delta; concurrent calls are serialized, and delivered
// EffectSequence values increase with gaps for dropped payloads. It returns no
// observer error. With no DeltaListener the emitter is nil, so Dispatchers must
// guard emission with emit != nil. A Dispatcher must join concurrent emissions
// before returning and must not retain or call emit afterward.
type DeltaEmitter func(payload json.RawMessage)

// Dispatcher executes Strategy-owned Effects outside Execution.Step. The Engine
// supplies valid requests and non-nil contexts; direct callers must do the
// same. Rejecting a valid request before external work starts is a definite
// Failed settlement. Implementations may serve Processes concurrently, must
// return in bounded time, and must not mutate an Execution or start unowned
// goroutines.
//
// A transparent decorator preserves the context, request identity, emitter,
// Settlement, and error of its wrapped Dispatcher, and forwards its
// EffectPolicy; it may add required capabilities for the work it adds.
// Forwarding a SameIdentity ReplayPolicy is valid only when the added work is
// safe to repeat under the original identity. Observation counters count dispatch attempts, including
// replay, not distinct logical operations; the Definition example shows one.
type Dispatcher interface {
	// Dispatch performs one frozen Effect and returns the Settlement answering
	// request; a non-nil error means the external outcome is unknown, not
	// definitely failed. emit is valid only during this call. The runtime
	// cancels ctx, which keeps Host values, when terminal intent reaches this
	// Process or an ancestor, and still collects the returned settlement. Panics
	// in Dispatch or in interpreting its error are isolated as unknown outcomes.
	// Explicit replay errors keep the original cause; Await, durable state, and
	// events retain only outcome classifications and bounded diagnostics.
	Dispatch(ctx context.Context, request EffectRequest, emit DeltaEmitter) (Settlement, error)
	// Policy declares, without I/O or mutable side effects, how the Engine
	// treats this exact Effect. The answer is deterministic for equivalent
	// Effects; the Engine asks again rather than retaining a copy.
	Policy(effect Effect) EffectPolicy
}

// EffectPolicy is the Dispatcher's declaration about one Effect it executes.
type EffectPolicy struct {
	// Replay states whether the Effect can be repeated under its original
	// EffectID when restoring a pending attempt or through
	// Process.ReplayUnknownEffect. A settled Unknown requires an explicit host
	// request; terminal intent forbids starting replay.
	Replay ReplayPolicy
	// RequiredCapabilities is the authority the Process must hold. Insufficient
	// authority rejects the entire Step before dispatch, and restoration rejects
	// a prepared Effect the restored Process could not have prepared.
	RequiredCapabilities CapabilitySet
}

func (e EffectPolicy) Valid() bool { return e.Replay.Valid() && e.RequiredCapabilities.Valid() }
