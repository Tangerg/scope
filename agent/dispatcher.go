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
	// ReplayPolicyInvalid is the invalid zero value.
	ReplayPolicyInvalid ReplayPolicy = ""
	// ReplayPolicyNever forbids automatic replay of a restored pending Effect.
	ReplayPolicyNever ReplayPolicy = "never"
	// ReplayPolicySameIdentity permits replay only with the original EffectID.
	ReplayPolicySameIdentity ReplayPolicy = "same_identity"
)

func (r ReplayPolicy) Valid() bool {
	return r == ReplayPolicyNever || r == ReplayPolicySameIdentity
}

func (r ReplayPolicy) String() string {
	if !r.Valid() {
		return invalidEnumName
	}
	return string(r)
}

// EffectRequest is the immutable dispatch context prepared by the Engine.
type EffectRequest struct {
	processID     ProcessID
	incarnationID TreeIncarnationID
	deploymentRef DeploymentRef
	relation      ProcessRelation
	stepSequence  uint64
	batchIndex    uint32
	id            EffectID
	effect        Effect
}

func (e EffectRequest) clone() EffectRequest {
	e.effect = e.effect.clone()
	return e
}

// Valid reports whether the request contains one complete Engine-minted
// dispatch identity and immutable Effect.
func (e EffectRequest) Valid() bool {
	return e.processID.Valid() && e.deploymentRef.Valid() && e.relation.Valid() &&
		e.relation.ProcessID() == e.processID && e.stepSequence > 0 && e.id.Valid() &&
		e.effect.Valid()
}

func newEffectRequest(
	processID ProcessID,
	incarnationID TreeIncarnationID,
	deploymentRef DeploymentRef,
	relation ProcessRelation,
	stepSequence uint64,
	batchIndex uint32,
	id EffectID,
	effect Effect,
) EffectRequest {
	return EffectRequest{
		processID: processID, incarnationID: incarnationID, deploymentRef: deploymentRef, relation: relation,
		stepSequence: stepSequence, batchIndex: batchIndex, id: id,
		effect: effect.clone(),
	}
}

// ProcessID returns the Process that owns the Effect.
func (e EffectRequest) ProcessID() ProcessID { return e.processID }

// TreeIncarnationID identifies the active durable writer for observation and
// correlation. Ephemeral requests return false. It does not participate in the
// Effect's stable idempotency identity, which remains ID across restoration.
func (e EffectRequest) TreeIncarnationID() (TreeIncarnationID, bool) {
	return e.incarnationID, e.incarnationID.Valid()
}

// DeploymentRef returns the exact behavior binding executing the Effect.
func (e EffectRequest) DeploymentRef() DeploymentRef { return e.deploymentRef }

// Relation returns the immutable Process tree location executing the Effect.
func (e EffectRequest) Relation() ProcessRelation { return e.relation }

// StepSequence returns the one-based Step sequence that declared the Effect.
func (e EffectRequest) StepSequence() uint64 { return e.stepSequence }

// BatchIndex returns the zero-based declaration order within the Step Effect batch.
func (e EffectRequest) BatchIndex() uint32 { return e.batchIndex }

// ID returns the stable identity assigned during Step preparation.
func (e EffectRequest) ID() EffectID { return e.id }

// Effect returns an independently owned copy of the frozen intent.
func (e EffectRequest) Effect() Effect { return e.effect.clone() }

// DeltaEmitter accepts Strategy-owned streaming payloads while Dispatch is
// active. The Engine validates, orders, bounds, and publishes each payload as a
// best-effort Delta. It intentionally returns no observer error. A Dispatcher
// must not retain or call it after Dispatch returns.
type DeltaEmitter func(payload json.RawMessage)

// Dispatcher executes Strategy-owned Effects outside Execution.Step. It must
// return a Settlement addressed to request.ID. A returned error means the Engine
// cannot prove the external result and records an unknown settlement. The same
// Dispatcher may serve Processes concurrently; implementations must be
// concurrency-safe, return in bounded time, not mutate an Execution, and not
// start unowned goroutines. ReplayPolicy must be a pure, deterministic
// declaration for the supplied immutable Effect.
// The Engine always supplies a non-nil context. Direct callers must do the same;
// a nil context is a programming error, not an unknown external outcome.
//
// A transparent decorator preserves the context, complete request identity, emitter,
// Settlement, and error of its wrapped Dispatcher. Its ReplayPolicy must also
// account for its own behavior: forwarding a SameIdentity claim is valid only
// when the added work is safe to repeat under the original identity. Observation
// counters may count dispatch attempts, including replay; they do not count
// distinct logical external operations. See the Definition example for a
// concurrency-safe attempt counter around a bound Dispatcher.
type Dispatcher interface {
	// Dispatch performs one frozen Strategy Effect outside Execution.Step.
	// Settlement must address request.ID; a non-nil error means the external
	// outcome is unknown, not definitely failed. emit is valid only during this
	// call. The runtime cancels ctx when it applies terminal intent to this
	// Process or an ancestor. It still collects the returned settlement; ctx
	// cancellation alone proves no external outcome. Host context values are
	// preserved. Implementations honor ctx and may be called concurrently.
	Dispatch(ctx context.Context, request EffectRequest, emit DeltaEmitter) (Settlement, error)
	// ReplayPolicy declares, without I/O or mutable side effects, whether this
	// exact Effect can be repeated under its original EffectID when restoring
	// a pending attempt. A settled Unknown requires explicit adjudication, and
	// terminal intent forbids replay. The answer is deterministic for equivalent
	// Effects.
	ReplayPolicy(effect Effect) ReplayPolicy
}
