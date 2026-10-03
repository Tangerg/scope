package agent

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"

	"github.com/Tangerg/scope/agent/internal/jsonwire"
)

var ErrInvalidChildStart = errors.New("agent: invalid child process start")

// DeploymentResolver performs one bounded, deterministic, context-free lookup
// of an exact immutable Deployment for a child named by runtime input; the
// Engine never consults it for a Deployment's own static bindings. The Engine accepts only a result whose
// reference exactly matches the requested reference. Implementations must be
// safe for concurrent use, must not perform remote I/O, and must not re-enter
// any Process. Routing and caller-specific selection happen before an exact
// DeploymentRef reaches this contract.
type DeploymentResolver interface {
	// Resolve returns the immutable binding for exactly reference, without
	// falling back by name. Missing and mismatched bindings are errors.
	Resolve(reference DeploymentRef) (Deployment, error)
}

// ChildSpec is the complete Strategy-declared intent for one child Process.
// Input is validated by the target Deployment before any Process is created.
type ChildSpec struct {
	// Key is the parent-scoped logical identity of this child start.
	Key           ChildKey      `json:"key"`
	DeploymentRef DeploymentRef `json:"deployment_ref"`
	Input         Payload       `json:"input"`
	// Budget is permanently allocated from the parent to this child.
	Budget Budget `json:"budget"`
	// Capabilities is the attenuated authority granted to this child.
	Capabilities CapabilitySet `json:"capabilities"`
}

func (c ChildSpec) Valid() bool {
	return c.Key.Valid() && c.DeploymentRef.Valid() && c.Input.Valid() &&
		c.Capabilities.Valid()
}

// NewChildStartEffect creates a Framework-owned Effect requesting one independently
// managed child Process. The Engine derives the child ProcessID; Execution code
// cannot construct or start the Process directly.
func NewChildStartEffect(spec ChildSpec) (Effect, error) {
	if !spec.Valid() {
		return Effect{}, ErrInvalidChildStart
	}
	return newFrameworkEffect(childStartEffectWire{Operation: frameworkOperationStartChild, Spec: spec})
}

// ParseChildStartEffect returns the ChildSpec a NewChildStartEffect Effect
// declares, so a settlement can be read against the request that owns its
// key and Deployment.
func ParseChildStartEffect(effect Effect) (ChildSpec, error) {
	if !effect.Valid() || effect.Target() != EffectTargetFramework {
		return ChildSpec{}, ErrInvalidChildStart
	}
	return decodeChildStartEffect(effect.payload)
}

// ChildStartResult is the definite result of one NewChildStartEffect Effect. Success
// contains the Engine-created child ProcessID; failure contains a stable
// Framework Failure and never masquerades as an unknown external outcome. The
// requesting ChildSpec owns the key and Deployment; the result carries only
// what the start established.
type ChildStartResult struct {
	processID ProcessID
	failure   Failure
}

func (c ChildStartResult) ProcessID() (ProcessID, bool) {
	return c.processID, c.processID.Valid()
}

// Failure returns the definite start failure and true when no child was created.
func (c ChildStartResult) Failure() (Failure, bool) {
	return c.failure, c.failure.Valid()
}

func (c ChildStartResult) Valid() bool {
	return c.processID.Valid() != c.failure.Valid()
}

// settlementStatus is the status of the child-start Effect that returns c.
func (c ChildStartResult) settlementStatus() SettlementStatus {
	if c.failure.Valid() {
		return SettlementStatusFailed
	}
	return SettlementStatusSucceeded
}

func (c ChildStartResult) MarshalJSON() ([]byte, error) {
	if !c.Valid() {
		return nil, ErrInvalidChildStart
	}
	wire := childStartResultWire{Operation: frameworkOperationStartChild}
	if c.processID.Valid() {
		wire.ProcessID = &c.processID
	} else {
		wire.Failure = &c.failure
	}
	return jsonv2.Marshal(wire)
}

func (c *ChildStartResult) UnmarshalJSON(data []byte) error {
	if c == nil {
		return ErrInvalidChildStart
	}
	value, err := decodeChildStartResult(data)
	if err != nil {
		return err
	}
	*c = value
	return nil
}

func (ChildStartResult) JSONSchemaAlias() any { return childStartResultWire{} }

// ParseChildStartResult decodes a Framework-owned child-start settlement
// Signal. The Signal must carry an Engine-owned identity and must not address a wait.
func ParseChildStartResult(signal Signal) (ChildStartResult, error) {
	if !signal.EngineOwned() {
		return ChildStartResult{}, ErrInvalidSignal
	}
	if _, addressed := signal.WaitID(); addressed {
		return ChildStartResult{}, fmt.Errorf("%w: child-start Signal addresses a wait", ErrInvalidChildStart)
	}
	return decodeChildStartResult(signal.Payload())
}

type childStartEffectWire struct {
	Operation frameworkOperationKind `json:"operation"`
	Spec      ChildSpec              `json:"spec"`
}

type childStartResultWire struct {
	Operation frameworkOperationKind `json:"operation"`
	ProcessID *ProcessID             `json:"process_id,omitzero"`
	Failure   *Failure               `json:"failure,omitzero"`
}

func decodeChildStartEffect(payload json.RawMessage) (ChildSpec, error) {
	wire, err := jsonwire.Decode[childStartEffectWire](payload)
	if err != nil {
		return ChildSpec{}, fmt.Errorf("%w: decode start request: %w", ErrInvalidChildStart, err)
	}
	if wire.Operation != frameworkOperationStartChild || !wire.Spec.Valid() {
		return ChildSpec{}, ErrInvalidChildStart
	}
	return wire.Spec, nil
}

func decodeChildStartResult(payload json.RawMessage) (ChildStartResult, error) {
	wire, err := jsonwire.Decode[childStartResultWire](payload)
	if err != nil {
		return ChildStartResult{}, fmt.Errorf("%w: decode start result: %w", ErrInvalidChildStart, err)
	}
	var processID ProcessID
	if wire.ProcessID != nil {
		processID = *wire.ProcessID
	}
	var failure Failure
	if wire.Failure != nil {
		failure = *wire.Failure
	}
	result := ChildStartResult{processID: processID, failure: failure}
	if wire.Operation != frameworkOperationStartChild || !result.Valid() {
		return ChildStartResult{}, ErrInvalidChildStart
	}
	return result, nil
}
