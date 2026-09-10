package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

var ErrInvalidEffect = errors.New("agent: invalid effect")

// EffectTarget identifies which of the two execution boundaries owns an Effect.
// Framework Effects are interpreted by the Engine; Dispatcher Effects remain
// opaque to the Engine and are interpreted by the Deployment-bound dispatcher.
type EffectTarget string

const (
	// EffectTargetInvalid is the invalid zero value.
	EffectTargetInvalid EffectTarget = ""
	// EffectTargetFramework identifies an Engine-interpreted Effect.
	EffectTargetFramework EffectTarget = "framework"
	// EffectTargetDispatcher identifies a Strategy dispatcher Effect.
	EffectTargetDispatcher EffectTarget = "dispatcher"
)

func (e EffectTarget) Valid() bool {
	return e == EffectTargetFramework || e == EffectTargetDispatcher
}

func (e EffectTarget) String() string {
	if !e.Valid() {
		return invalidEnumName
	}
	return string(e)
}

// Effect is an immutable request for an operation outside Execution.Step.
// Payload is frozen before dispatch and interpreted only by Target's owner.
// EffectID is deliberately absent because the Engine assigns it during prepare.
type Effect struct {
	target       EffectTarget
	payload      json.RawMessage
	requirements CapabilitySet
}

// NewDispatcherEffect carries an opaque payload plus the capabilities it
// requires, so the Engine can refuse an effect the Process was never granted
// without understanding what the effect does. Keeping the payload opaque is
// what stops model and tool vocabulary from entering the kernel.
func NewDispatcherEffect(payload json.RawMessage, required ...Capability) (Effect, error) {
	requirements, err := NewCapabilitySet(required...)
	if err != nil {
		return Effect{}, fmt.Errorf("%w: required capabilities: %w", ErrInvalidEffect, err)
	}
	return newEffectWithCapabilities(EffectTargetDispatcher, payload, requirements)
}

// RequestWait creates the Framework Effect that asks the Engine to mint one
// WaitID for key. signalPayload remains Strategy-owned and is returned unchanged
// in the internal Signal that carries the minted WaitID back to the Execution.
func RequestWait(key WaitKey, signalPayload json.RawMessage) (Effect, error) {
	if !key.Valid() {
		return Effect{}, fmt.Errorf("%w: wait key: %w", ErrInvalidEffect, ErrInvalidIdentity)
	}
	normalized, err := wireJSON.normalize(signalPayload, maxWireBytes)
	if err != nil {
		return Effect{}, fmt.Errorf("%w: wait signal payload: %w", ErrInvalidEffect, err)
	}
	payload, err := json.Marshal(waitRequestWire{
		Operation:     frameworkEffectWait,
		Key:           key,
		SignalPayload: normalized,
	})
	if err != nil {
		return Effect{}, fmt.Errorf("%w: encode wait request: %w", ErrInvalidEffect, err)
	}
	return newEffect(EffectTargetFramework, payload)
}

func newEffect(target EffectTarget, payload json.RawMessage) (Effect, error) {
	return newEffectWithCapabilities(target, payload, CapabilitySet{})
}

func newEffectWithCapabilities(
	target EffectTarget,
	payload json.RawMessage,
	requirements CapabilitySet,
) (Effect, error) {
	if !target.Valid() {
		return Effect{}, fmt.Errorf("%w: invalid target", ErrInvalidEffect)
	}
	if !requirements.Valid() || target == EffectTargetFramework && len(requirements.values) != 0 {
		return Effect{}, fmt.Errorf("%w: invalid required capabilities", ErrInvalidEffect)
	}
	normalized, err := wireJSON.normalize(payload, maxWireBytes)
	if err != nil {
		return Effect{}, fmt.Errorf("%w: payload: %w", ErrInvalidEffect, err)
	}
	if target == EffectTargetFramework {
		if err := validateFrameworkEffectPayload(normalized); err != nil {
			return Effect{}, err
		}
	}
	return Effect{target: target, payload: normalized, requirements: requirements}, nil
}

// Target returns the owner responsible for interpreting Payload.
func (e Effect) Target() EffectTarget { return e.target }

// Payload returns an independently owned copy of the operation intent.
func (e Effect) Payload() json.RawMessage { return bytes.Clone(e.payload) }

// RequiredCapabilities returns the immutable authority set the Process must
// possess before this Dispatcher Effect may be prepared.
func (e Effect) RequiredCapabilities() CapabilitySet { return e.requirements }

func (e Effect) Valid() bool {
	return e.target.Valid() &&
		len(e.payload) > 0 && e.requirements.Valid() &&
		(e.target != EffectTargetFramework || len(e.requirements.values) == 0)
}

func (e Effect) clone() Effect {
	requirements, _ := NewCapabilitySet(e.requirements.values...)
	return Effect{target: e.target, payload: bytes.Clone(e.payload), requirements: requirements}
}

func (e Effect) MarshalJSON() ([]byte, error) {
	if !e.Valid() {
		return nil, ErrInvalidEffect
	}
	return json.Marshal(effectWire{
		Target: e.target, Payload: e.payload,
		RequiredCapabilities: e.requirements.Values(),
	})
}

func (e *Effect) UnmarshalJSON(data []byte) error {
	if e == nil {
		return fmt.Errorf("%w: nil receiver", ErrInvalidEffect)
	}
	wire, err := wireJSON.decode[effectWire](data)
	if err != nil {
		return fmt.Errorf("%w: decode: %w", ErrInvalidEffect, err)
	}
	requirements, err := NewCapabilitySet(wire.RequiredCapabilities...)
	if err != nil {
		return fmt.Errorf("%w: required capabilities: %w", ErrInvalidEffect, err)
	}
	value, err := newEffectWithCapabilities(wire.Target, wire.Payload, requirements)
	if err != nil {
		return err
	}
	*e = value
	return nil
}

type effectWire struct {
	Target               EffectTarget    `json:"target"`
	Payload              json.RawMessage `json:"payload"`
	RequiredCapabilities []Capability    `json:"required_capabilities,omitempty"`
}

type frameworkEffectOperation string

const (
	frameworkEffectWait         frameworkEffectOperation = "wait"
	frameworkEffectStartChild   frameworkEffectOperation = "start_child"
	frameworkEffectWaitChildren frameworkEffectOperation = "wait_children"
	frameworkEffectSignalChild  frameworkEffectOperation = "signal_child"
	frameworkEffectCancelChild  frameworkEffectOperation = "cancel_child"
)

func (f frameworkEffectOperation) valid() bool {
	switch f {
	case frameworkEffectWait, frameworkEffectStartChild, frameworkEffectWaitChildren,
		frameworkEffectSignalChild, frameworkEffectCancelChild:
		return true
	default:
		return false
	}
}

type waitRequestWire struct {
	Operation     frameworkEffectOperation `json:"operation"`
	Key           WaitKey                  `json:"key"`
	SignalPayload json.RawMessage          `json:"signal_payload"`
}

func decodeWaitRequest(effect Effect) (WaitKey, json.RawMessage, error) {
	if effect.Target() != EffectTargetFramework {
		return WaitKey{}, nil, fmt.Errorf("%w: Effect is not framework-owned", ErrInvalidEffect)
	}
	return decodeWaitRequestPayload(effect.payload)
}

func decodeWaitRequestPayload(payload json.RawMessage) (WaitKey, json.RawMessage, error) {
	wire, err := wireJSON.decode[waitRequestWire](payload)
	if err != nil {
		return WaitKey{}, nil, fmt.Errorf("%w: decode Framework Effect: %w", ErrInvalidEffect, err)
	}
	if wire.Operation != frameworkEffectWait || !wire.Key.Valid() {
		return WaitKey{}, nil, fmt.Errorf("%w: unsupported Framework Effect", ErrInvalidEffect)
	}
	normalized, err := wireJSON.normalize(wire.SignalPayload, maxWireBytes)
	if err != nil {
		return WaitKey{}, nil, fmt.Errorf("%w: framework Effect signal payload: %w", ErrInvalidEffect, err)
	}
	return wire.Key, normalized, nil
}

func validateFrameworkEffectPayload(payload json.RawMessage) error {
	operation, err := decodeFrameworkEffectOperation(payload)
	if err != nil {
		return err
	}
	switch operation {
	case frameworkEffectWait:
		_, _, err := decodeWaitRequestPayload(payload)
		return err
	case frameworkEffectStartChild:
		_, err := decodeChildStartEffect(payload)
		return err
	case frameworkEffectWaitChildren:
		_, err := decodeChildWaitEffect(payload)
		return err
	case frameworkEffectSignalChild, frameworkEffectCancelChild:
		_, err := decodeChildControlEffect(payload)
		return err
	default:
		return fmt.Errorf("%w: unsupported Framework Effect", ErrInvalidEffect)
	}
}

func decodeFrameworkEffectOperation(payload json.RawMessage) (frameworkEffectOperation, error) {
	var header struct {
		Operation frameworkEffectOperation `json:"operation"`
	}
	if err := json.Unmarshal(payload, &header); err != nil {
		return "", fmt.Errorf("%w: decode Framework Effect header: %w", ErrInvalidEffect, err)
	}
	if !header.Operation.valid() {
		return "", fmt.Errorf("%w: unsupported Framework Effect", ErrInvalidEffect)
	}
	return header.Operation, nil
}
