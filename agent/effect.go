package agent

import (
	"bytes"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"slices"

	"github.com/Tangerg/scope/agent/internal/jsonwire"
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
	switch e {
	case EffectTargetFramework, EffectTargetDispatcher:
		return true
	default:
		return false
	}
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
	return freezeEffect(EffectTargetDispatcher, payload, requirements)
}

// NewWaitEffect creates the Framework Effect that asks the Engine to mint one
// WaitID for key. signalPayload remains Strategy-owned and is returned unchanged
// in the internal Signal that carries the minted WaitID back to the Execution.
func NewWaitEffect(key WaitKey, signalPayload json.RawMessage) (Effect, error) {
	if !key.Valid() {
		return Effect{}, fmt.Errorf("%w: wait key: %w", ErrInvalidEffect, ErrInvalidIdentity)
	}
	normalized, err := normalizeJSON(signalPayload, MaxPayloadBytes)
	if err != nil {
		return Effect{}, fmt.Errorf("%w: wait signal payload: %w", ErrInvalidEffect, err)
	}
	return newFrameworkEffect(waitRequestWire{
		Operation: frameworkEffectWait, Key: key, SignalPayload: normalized,
	})
}

// Typed Framework constructors validate their domain requests before encoding.
// Only UnmarshalJSON crosses an untrusted protocol boundary and must decode
// those fields again. freezeEffect owns JSON validity, size, and immutability.
func newFrameworkEffect(request any) (Effect, error) {
	payload, err := json.Marshal(request)
	if err != nil {
		return Effect{}, fmt.Errorf("%w: encode Framework request: %w", ErrInvalidEffect, err)
	}
	return freezeEffect(EffectTargetFramework, payload, CapabilitySet{})
}

func freezeEffect(
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
	normalized, err := normalizeJSON(payload, MaxPayloadBytes)
	if err != nil {
		return Effect{}, fmt.Errorf("%w: payload: %w", ErrInvalidEffect, err)
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
	return Effect{target: e.target, payload: bytes.Clone(e.payload), requirements: e.requirements}
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
	wire, err := jsonwire.Decode[effectWire](data)
	if err != nil {
		return fmt.Errorf("%w: decode: %w", ErrInvalidEffect, err)
	}
	requirements, err := NewCapabilitySet(wire.RequiredCapabilities...)
	if err != nil {
		return fmt.Errorf("%w: required capabilities: %w", ErrInvalidEffect, err)
	}
	value, err := freezeEffect(wire.Target, wire.Payload, requirements)
	if err != nil {
		return err
	}
	if value.target == EffectTargetFramework {
		if err := validateFrameworkEffectPayload(value.payload); err != nil {
			return err
		}
	}
	*e = value
	return nil
}

func (e Effect) waitRequest() (WaitKey, json.RawMessage, error) {
	if e.Target() != EffectTargetFramework {
		return WaitKey{}, nil, fmt.Errorf("%w: Effect is not framework-owned", ErrInvalidEffect)
	}
	return decodeWaitRequestPayload(e.payload)
}

func (e Effect) equal(other Effect) bool {
	return e.Valid() && other.Valid() && e.target == other.target &&
		bytes.Equal(e.payload, other.payload) && slices.Equal(e.requirements.values, other.requirements.values)
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

func decodeWaitRequestPayload(payload json.RawMessage) (WaitKey, json.RawMessage, error) {
	wire, err := jsonwire.Decode[waitRequestWire](payload)
	if err != nil {
		return WaitKey{}, nil, fmt.Errorf("%w: decode Framework Effect: %w", ErrInvalidEffect, err)
	}
	if wire.Operation != frameworkEffectWait || !wire.Key.Valid() {
		return WaitKey{}, nil, fmt.Errorf("%w: unsupported Framework Effect", ErrInvalidEffect)
	}
	// The enclosing Effect owns canonicalization and the byte bound. Decoding
	// its RawMessage preserves those bytes without a second normalization.
	if len(wire.SignalPayload) == 0 {
		return WaitKey{}, nil, ErrInvalidEffect
	}
	return wire.Key, wire.SignalPayload, nil
}

func validateFrameworkEffectPayload(payload json.RawMessage) error {
	_, err := decodeFrameworkOperation(payload)
	return err
}

// The header deliberately accepts operation-owned fields; the selected strict
// decoder below owns their validation.
type frameworkEffectHeader struct {
	Operation frameworkEffectOperation `json:"operation"`
}

func decodeFrameworkEffectOperation(payload json.RawMessage) (frameworkEffectOperation, error) {
	var header frameworkEffectHeader
	if err := jsonv2.Unmarshal(payload, &header); err != nil {
		return "", fmt.Errorf("%w: decode Framework Effect header: %w", ErrInvalidEffect, err)
	}
	if !header.Operation.valid() {
		return "", fmt.Errorf("%w: unsupported Framework Effect", ErrInvalidEffect)
	}
	return header.Operation, nil
}
