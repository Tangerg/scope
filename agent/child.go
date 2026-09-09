package agent

import (
	"encoding/json"
	"errors"
	"fmt"
)

var ErrInvalidChildStart = errors.New("agent: invalid child process start")

// DeploymentResolver performs one bounded, deterministic, context-free lookup
// of an exact immutable Deployment. The Engine accepts only a result whose
// reference exactly matches the requested reference. Implementations must be
// safe for concurrent use, must not perform remote I/O, and must not re-enter
// any Process. Routing and caller-specific selection happen before an exact
// DeploymentRef reaches this contract.
type DeploymentResolver interface {
	// Resolve returns the immutable binding for exactly reference. It must not
	// fall back by name, perform routing or remote discovery, or retain
	// caller state. Missing and mismatched bindings are errors; concurrent calls
	// must be safe.
	Resolve(reference DeploymentRef) (Deployment, error)
}

// ChildSpec is the complete Strategy-declared intent for one child Process.
// Input is validated by the target Deployment before any Process is created.
type ChildSpec struct {
	// Key is the parent-scoped logical identity of this child start.
	Key ChildKey `json:"key"`
	// DeploymentRef identifies the exact child behavior binding.
	DeploymentRef DeploymentRef `json:"deployment_ref"`
	// Input is the portable input validated by the target Descriptor.
	Input Input `json:"input"`
	// Budget is permanently allocated from the parent to this child.
	Budget Budget `json:"budget"`
	// Capabilities is the attenuated authority granted to this child.
	Capabilities CapabilitySet `json:"capabilities"`
}

func (c ChildSpec) Valid() bool {
	return c.Key.Valid() && c.DeploymentRef.Valid() && c.Input.Valid() &&
		c.Budget.Valid() && c.Capabilities.Valid()
}

// StartChild creates a Framework-owned Effect requesting one independently
// managed child Process. The Engine derives the child ProcessID; Execution code
// cannot construct or start the Process directly.
func StartChild(spec ChildSpec) (Effect, error) {
	if !spec.Valid() {
		return Effect{}, ErrInvalidChildStart
	}
	payload, err := json.Marshal(childStartEffectWire{
		Operation: frameworkEffectStartChild,
		Spec:      spec,
	})
	if err != nil {
		return Effect{}, fmt.Errorf("%w: encode start request: %w", ErrInvalidChildStart, err)
	}
	return newEffect(EffectTargetFramework, payload)
}

// ChildStartResult is the definite result of one StartChild Effect. Success
// contains the Engine-created child ProcessID; failure contains a stable
// Framework Failure and never masquerades as an unknown external outcome.
type ChildStartResult struct {
	key           ChildKey
	processID     ProcessID
	deploymentRef DeploymentRef
	failure       Failure
}

// Key returns the logical child identity declared by the Execution.
func (c ChildStartResult) Key() ChildKey { return c.key }

// ProcessID returns the created child identity and true on success.
func (c ChildStartResult) ProcessID() (ProcessID, bool) {
	return c.processID, c.processID.Valid() && !c.failure.Valid()
}

// DeploymentRef returns the exact child execution binding.
func (c ChildStartResult) DeploymentRef() DeploymentRef { return c.deploymentRef }

// Failure returns the definite start failure and true when no child was
// created.
func (c ChildStartResult) Failure() (Failure, bool) {
	return c.failure, c.failure.Valid() && !c.processID.Valid()
}

func (c ChildStartResult) Valid() bool {
	return c.key.Valid() && c.deploymentRef.Valid() &&
		(c.processID.Valid() != c.failure.Valid())
}

func (c ChildStartResult) MarshalJSON() ([]byte, error) { return encodeChildStartResult(c) }

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
// Signal. The Signal must not address a wait.
func ParseChildStartResult(signal Signal) (ChildStartResult, error) {
	if !signal.Valid() {
		return ChildStartResult{}, ErrInvalidSignal
	}
	if _, addressed := signal.WaitID(); addressed {
		return ChildStartResult{}, fmt.Errorf("%w: child-start Signal addresses a wait", ErrInvalidChildStart)
	}
	return decodeChildStartResult(signal.Payload())
}

type childStartEffectWire struct {
	Operation frameworkEffectOperation `json:"operation"`
	Spec      ChildSpec                `json:"spec"`
}

type childStartResultWire struct {
	Operation     frameworkEffectOperation `json:"operation"`
	Key           ChildKey                 `json:"key"`
	ProcessID     *ProcessID               `json:"process_id,omitempty"`
	DeploymentRef DeploymentRef            `json:"deployment_ref"`
	Failure       *Failure                 `json:"failure,omitempty"`
}

func decodeChildStartEffect(payload json.RawMessage) (ChildSpec, error) {
	wire, err := wireJSON.decode[childStartEffectWire](payload)
	if err != nil {
		return ChildSpec{}, fmt.Errorf("%w: decode start request: %w", ErrInvalidChildStart, err)
	}
	if wire.Operation != frameworkEffectStartChild || !wire.Spec.Valid() {
		return ChildSpec{}, ErrInvalidChildStart
	}
	return wire.Spec, nil
}

func encodeChildStartResult(result ChildStartResult) (json.RawMessage, error) {
	if !result.Valid() {
		return nil, ErrInvalidChildStart
	}
	wire := childStartResultWire{
		Operation:     frameworkEffectStartChild,
		Key:           result.key,
		DeploymentRef: result.deploymentRef,
	}
	if result.processID.Valid() {
		processID := result.processID
		wire.ProcessID = &processID
	} else {
		failure := result.failure
		wire.Failure = &failure
	}
	payload, err := json.Marshal(wire)
	if err != nil {
		return nil, fmt.Errorf("%w: encode start result: %w", ErrInvalidChildStart, err)
	}
	return payload, nil
}

func decodeChildStartResult(payload json.RawMessage) (ChildStartResult, error) {
	wire, err := wireJSON.decode[childStartResultWire](payload)
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
	result := ChildStartResult{
		key: wire.Key, processID: processID, deploymentRef: wire.DeploymentRef, failure: failure,
	}
	if wire.Operation != frameworkEffectStartChild || !result.Valid() {
		return ChildStartResult{}, ErrInvalidChildStart
	}
	return result, nil
}
