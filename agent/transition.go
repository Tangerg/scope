package agent

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"slices"

	"github.com/Tangerg/scope/agent/internal/jsonwire"
)

var ErrInvalidTransition = errors.New("agent: invalid transition")

type TransitionKind string

const (
	TransitionKindInvalid    TransitionKind = ""
	TransitionKindContinue   TransitionKind = "continue"
	TransitionKindCheckpoint TransitionKind = "checkpoint"
	TransitionKindWait       TransitionKind = "wait"
	TransitionKindPause      TransitionKind = "pause"
	TransitionKindComplete   TransitionKind = "complete"
	TransitionKindFail       TransitionKind = "fail"
)

func (t TransitionKind) Valid() bool {
	switch t {
	case TransitionKindContinue, TransitionKindCheckpoint, TransitionKindWait, TransitionKindPause,
		TransitionKindComplete, TransitionKindFail:
		return true
	default:
		return false
	}
}

func (t TransitionKind) String() string {
	if !t.Valid() {
		return invalidEnumName
	}
	return string(t)
}

// Transition is an immutable candidate lifecycle intent. The Engine validates
// ConsumedSignals against the delivered Signal window, captures the candidate
// ExecutionState, and assigns EffectID values before committing anything.
type Transition struct {
	kind            TransitionKind
	consumedSignals uint32
	effects         []Effect
	waitID          WaitID
	pause           pause
	output          Payload
	failure         Failure
}

// Continue keeps the Process schedulable after consuming the stated Signal
// prefix. Effects are dispatched only after the Engine prepares the Step.
func Continue(consumedSignals uint32, effects ...Effect) (Transition, error) {
	owned, err := cloneEffects(effects)
	if err != nil {
		return Transition{}, err
	}
	return Transition{kind: TransitionKindContinue, consumedSignals: consumedSignals, effects: owned}, nil
}

// Checkpoint commits the consumed Signal prefix and candidate state; the
// TreeCommitter must acknowledge the complete tree before any further Step or
// Effect in this Process runs. It creates no settlement Signal.
func Checkpoint(consumedSignals uint32) (Transition, error) {
	return Transition{kind: TransitionKindCheckpoint, consumedSignals: consumedSignals}, nil
}

// Wait moves the Process to Waiting for an Engine-minted WaitID already stored
// in the candidate ExecutionState.
func Wait(consumedSignals uint32, waitID WaitID) (Transition, error) {
	if !waitID.Valid() {
		return Transition{}, fmt.Errorf("%w: wait ID: %w", ErrInvalidTransition, ErrInvalidIdentity)
	}
	return Transition{kind: TransitionKindWait, consumedSignals: consumedSignals, waitID: waitID}, nil
}

func Pause(consumedSignals uint32, reason string) (Transition, error) {
	requested, err := newPause(reason)
	if err != nil {
		return Transition{}, fmt.Errorf("%w: %w", ErrInvalidTransition, err)
	}
	return Transition{kind: TransitionKindPause, consumedSignals: consumedSignals, pause: requested}, nil
}

// Complete supplies the final semantic Output. The Engine must validate it
// against the Definition Descriptor before committing Completed.
func Complete(consumedSignals uint32, output Payload) (Transition, error) {
	if !output.Valid() {
		return Transition{}, fmt.Errorf("%w: output: %w", ErrInvalidTransition, ErrInvalidPayload)
	}
	return Transition{kind: TransitionKindComplete, consumedSignals: consumedSignals, output: output}, nil
}

// Fail supplies a stable Strategy-declared failure without making the
// Execution instance untrusted. A Step error follows the separate discard path.
func Fail(consumedSignals uint32, failure Failure) (Transition, error) {
	if !failure.Valid() {
		return Transition{}, fmt.Errorf("%w: failure: %w", ErrInvalidTransition, ErrInvalidFailure)
	}
	return Transition{kind: TransitionKindFail, consumedSignals: consumedSignals, failure: failure}, nil
}

func (t Transition) Kind() TransitionKind { return t.kind }

func (t Transition) ConsumedSignals() uint32 { return t.consumedSignals }

// Effects returns independently owned operation intents in declaration order.
func (t Transition) Effects() []Effect { return cloneEffectsUnchecked(t.effects) }

func (t Transition) WaitID() (WaitID, bool) { return t.waitID, t.kind == TransitionKindWait }

func (t Transition) Reason() (string, bool) { return t.pause.reason, t.kind == TransitionKindPause }

func (t Transition) Output() (Payload, bool) { return t.output, t.kind == TransitionKindComplete }

func (t Transition) Failure() (Failure, bool) { return t.failure, t.kind == TransitionKindFail }

// Valid requires each kind to carry exactly its own payload field.
func (t Transition) Valid() bool {
	if !t.kind.Valid() {
		return false
	}
	if len(t.effects) != 0 && (t.kind != TransitionKindContinue || !validEffects(t.effects)) {
		return false
	}
	return t.waitID.Valid() == (t.kind == TransitionKindWait) &&
		t.pause.valid() == (t.kind == TransitionKindPause) &&
		t.output.Valid() == (t.kind == TransitionKindComplete) &&
		t.failure.Valid() == (t.kind == TransitionKindFail)
}

func cloneEffects(effects []Effect) ([]Effect, error) {
	for _, effect := range effects {
		if !effect.Valid() {
			return nil, fmt.Errorf("%w: effect: %w", ErrInvalidTransition, ErrInvalidEffect)
		}
	}
	return cloneEffectsUnchecked(effects), nil
}

func cloneEffectsUnchecked(effects []Effect) []Effect {
	owned := slices.Clone(effects)
	for index := range owned {
		owned[index] = owned[index].clone()
	}
	return owned
}

func validEffects(effects []Effect) bool {
	for _, effect := range effects {
		if !effect.Valid() {
			return false
		}
	}
	return true
}

func (t Transition) MarshalJSON() ([]byte, error) {
	if !t.Valid() {
		return nil, ErrInvalidTransition
	}
	wire := transitionWire{Kind: t.kind, ConsumedSignals: t.consumedSignals}
	switch t.kind {
	case TransitionKindContinue:
		wire.Effects = t.effects
	case TransitionKindWait:
		wire.WaitID = &t.waitID
	case TransitionKindPause:
		wire.Reason = t.pause.reason
	case TransitionKindComplete:
		wire.Output = t.output.JSON()
	case TransitionKindFail:
		wire.Failure = &t.failure
	}
	return jsonv2.Marshal(wire)
}

func (t *Transition) UnmarshalJSON(data []byte) error {
	if t == nil {
		return fmt.Errorf("%w: nil receiver", ErrInvalidTransition)
	}
	wire, err := jsonwire.Decode[transitionWire](data)
	if err != nil {
		return fmt.Errorf("%w: decode: %w", ErrInvalidTransition, err)
	}
	value, err := transitionFromWire(wire)
	if err != nil {
		return err
	}
	*t = value
	return nil
}

func transitionFromWire(wire transitionWire) (Transition, error) {
	requested, err := parsePause(wire.Reason)
	if err != nil {
		return Transition{}, fmt.Errorf("%w: %w", ErrInvalidTransition, err)
	}
	value := Transition{kind: wire.Kind, consumedSignals: wire.ConsumedSignals,
		effects: wire.Effects, pause: requested}
	if wire.WaitID != nil {
		value.waitID = *wire.WaitID
	}
	if wire.Failure != nil {
		value.failure = *wire.Failure
	}
	if len(wire.Output) != 0 {
		output, err := ParsePayload(wire.Output)
		if err != nil {
			return Transition{}, fmt.Errorf("%w: output: %w", ErrInvalidTransition, err)
		}
		value.output = output
	}
	if !value.Valid() {
		return Transition{}, fmt.Errorf("%w: invalid kind or field set", ErrInvalidTransition)
	}
	return value, nil
}

type transitionWire struct {
	Kind            TransitionKind  `json:"kind"`
	ConsumedSignals uint32          `json:"consumed_signals"`
	Effects         []Effect        `json:"effects,omitempty"`
	WaitID          *WaitID         `json:"wait_id,omitzero"`
	Reason          string          `json:"reason,omitempty"`
	Output          json.RawMessage `json:"output,omitzero"`
	Failure         *Failure        `json:"failure,omitzero"`
}
