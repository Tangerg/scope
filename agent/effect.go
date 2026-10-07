package agent

import (
	"bytes"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/agent/internal/jsonwire"
)

var ErrInvalidEffect = errors.New("agent: invalid effect")

// EffectTarget identifies which of the two execution boundaries owns an Effect.
// Framework Effects are interpreted by the Engine; Dispatcher Effects remain
// opaque to the Engine and are interpreted by the Deployment-bound dispatcher.
type EffectTarget string

const (
	EffectTargetInvalid    EffectTarget = ""
	EffectTargetFramework  EffectTarget = "framework"
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
	target  EffectTarget
	payload json.RawMessage
}

// NewDispatcherEffect freezes an opaque request. Its Dispatcher's
// [EffectPolicy] declares the authority it requires.
func NewDispatcherEffect(payload json.RawMessage) (Effect, error) {
	return freezeEffect(EffectTargetDispatcher, payload)
}

// NewWaitEffect creates the Framework Effect that asks the Engine to mint one
// WaitID for key. ParseWaitOpened reads the minted WaitID from the Signal that
// acknowledges it; the Execution keeps whatever the wait is about.
func NewWaitEffect(key WaitKey) (Effect, error) {
	if !key.Valid() {
		return Effect{}, fmt.Errorf("%w: wait key: %w", ErrInvalidEffect, ErrInvalidIdentity)
	}
	return newFrameworkEffect(waitRequestWire{Operation: frameworkOperationWait, Key: key})
}

// ParseWaitOpened returns the WaitID that a NewWaitEffect settlement Signal
// acknowledges, verifying its Engine-owned identity and fixed payload.
func ParseWaitOpened(signal Signal) (WaitID, error) {
	waitID, addressed := signal.WaitID()
	if !signal.EngineOwned() || !addressed || !bytes.Equal(signal.Payload(), waitOpenedPayload()) {
		return WaitID{}, fmt.Errorf("%w: not a wait opening", ErrInvalidSignal)
	}
	return waitID, nil
}

// waitOpenedPayload is the normalized payload of every external wait opening.
func waitOpenedPayload() json.RawMessage {
	return lo.Must(jsonv2.Marshal(frameworkOperationHeader{Operation: frameworkOperationWait}))
}

// Typed constructors validate their requests before encoding, so only
// UnmarshalJSON must decode Framework fields again.
func newFrameworkEffect(request any) (Effect, error) {
	payload, err := jsonv2.Marshal(request)
	if err != nil {
		return Effect{}, fmt.Errorf("%w: encode Framework request: %w", ErrInvalidEffect, err)
	}
	return freezeEffect(EffectTargetFramework, payload)
}

func freezeEffect(target EffectTarget, payload json.RawMessage) (Effect, error) {
	if !target.Valid() {
		return Effect{}, fmt.Errorf("%w: invalid target", ErrInvalidEffect)
	}
	normalized, err := normalizeJSON(payload, MaxPayloadBytes)
	if err != nil {
		return Effect{}, fmt.Errorf("%w: payload: %w", ErrInvalidEffect, err)
	}
	return Effect{target: target, payload: normalized}, nil
}

func (e Effect) Target() EffectTarget { return e.target }

// Payload returns an independently owned copy of the operation intent.
func (e Effect) Payload() json.RawMessage { return bytes.Clone(e.payload) }

func (e Effect) Valid() bool {
	return e.target.Valid() && len(e.payload) > 0
}

func (e Effect) clone() Effect {
	return Effect{target: e.target, payload: bytes.Clone(e.payload)}
}

func (e Effect) MarshalJSON() ([]byte, error) {
	if !e.Valid() {
		return nil, ErrInvalidEffect
	}
	return jsonv2.Marshal(effectWire{Target: e.target, Payload: e.payload})
}

func (e *Effect) UnmarshalJSON(data []byte) error {
	if e == nil {
		return fmt.Errorf("%w: nil receiver", ErrInvalidEffect)
	}
	wire, err := jsonwire.Decode[effectWire](data)
	if err != nil {
		return fmt.Errorf("%w: decode: %w", ErrInvalidEffect, err)
	}
	value, err := freezeEffect(wire.Target, wire.Payload)
	if err != nil {
		return err
	}
	if value.target == EffectTargetFramework {
		if _, err := decodeFrameworkOperation(value.payload); err != nil {
			return err
		}
	}
	*e = value
	return nil
}

func (e Effect) equal(other Effect) bool {
	return e.Valid() && other.Valid() && e.target == other.target &&
		bytes.Equal(e.payload, other.payload)
}

type effectWire struct {
	Target  EffectTarget    `json:"target"`
	Payload json.RawMessage `json:"payload"`
}

type frameworkOperationKind string

const (
	frameworkOperationWait         frameworkOperationKind = "wait"
	frameworkOperationStartChild   frameworkOperationKind = "start_child"
	frameworkOperationWaitChildren frameworkOperationKind = "wait_children"
	frameworkOperationSignalChild  frameworkOperationKind = "signal_child"
	frameworkOperationCancelChild  frameworkOperationKind = "cancel_child"
)

type waitRequestWire struct {
	Operation frameworkOperationKind `json:"operation"`
	Key       WaitKey                `json:"key"`
}

func decodeWaitRequestPayload(payload json.RawMessage) (WaitKey, error) {
	wire, err := jsonwire.Decode[waitRequestWire](payload)
	if err != nil {
		return WaitKey{}, fmt.Errorf("%w: decode Framework Effect: %w", ErrInvalidEffect, err)
	}
	if wire.Operation != frameworkOperationWait || !wire.Key.Valid() {
		return WaitKey{}, fmt.Errorf("%w: unsupported Framework Effect", ErrInvalidEffect)
	}
	return wire.Key, nil
}
