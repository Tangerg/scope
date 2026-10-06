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
//
// A Transition with a wait, pause, output, or failure is that kind; advance
// names the two kinds that carry no payload of their own.
type Transition struct {
	advance         TransitionKind
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
	return Transition{advance: TransitionKindContinue, consumedSignals: consumedSignals, effects: owned}, nil
}

// Checkpoint commits the consumed Signal prefix and candidate state; the
// TreeCommitter must acknowledge the complete tree before any further Step or
// Effect in this Process runs. It creates no settlement Signal.
func Checkpoint(consumedSignals uint32) (Transition, error) {
	return Transition{advance: TransitionKindCheckpoint, consumedSignals: consumedSignals}, nil
}

// Wait moves the Process to Waiting for an Engine-minted WaitID already stored
// in the candidate ExecutionState.
func Wait(consumedSignals uint32, waitID WaitID) (Transition, error) {
	if !waitID.Valid() {
		return Transition{}, fmt.Errorf("%w: wait ID: %w", ErrInvalidTransition, ErrInvalidIdentity)
	}
	return Transition{consumedSignals: consumedSignals, waitID: waitID}, nil
}

func Pause(consumedSignals uint32, reason string) (Transition, error) {
	requested, err := newPause(reason)
	if err != nil {
		return Transition{}, fmt.Errorf("%w: %w", ErrInvalidTransition, err)
	}
	return Transition{consumedSignals: consumedSignals, pause: requested}, nil
}

// Complete supplies the final semantic Output. The Engine must validate it
// against the Definition Descriptor before committing Completed.
func Complete(consumedSignals uint32, output Payload) (Transition, error) {
	if !output.Valid() {
		return Transition{}, fmt.Errorf("%w: output: %w", ErrInvalidTransition, ErrInvalidPayload)
	}
	return Transition{consumedSignals: consumedSignals, output: output}, nil
}

// Fail supplies a stable Strategy-declared failure without making the
// Execution instance untrusted. A Step error follows the separate discard path.
func Fail(consumedSignals uint32, failure Failure) (Transition, error) {
	if !failure.Valid() {
		return Transition{}, fmt.Errorf("%w: failure: %w", ErrInvalidTransition, ErrInvalidFailure)
	}
	return Transition{consumedSignals: consumedSignals, failure: failure}, nil
}

func (t Transition) Kind() TransitionKind {
	switch {
	case t.waitID.Valid():
		return TransitionKindWait
	case t.pause.valid():
		return TransitionKindPause
	case t.output.Valid():
		return TransitionKindComplete
	case t.failure.Valid():
		return TransitionKindFail
	default:
		return t.advance
	}
}

func (t Transition) ConsumedSignals() uint32 { return t.consumedSignals }

// Effects returns independently owned operation intents in declaration order.
func (t Transition) Effects() []Effect { return cloneEffectsUnchecked(t.effects) }

func (t Transition) WaitID() (WaitID, bool) { return t.waitID, t.waitID.Valid() }

func (t Transition) Reason() (string, bool) { return t.pause.reason, t.pause.valid() }

func (t Transition) Output() (Payload, bool) { return t.output, t.output.Valid() }

func (t Transition) Failure() (Failure, bool) { return t.failure, t.failure.Valid() }

// Valid requires exactly one variant: one payload, or one payload-free kind.
func (t Transition) Valid() bool {
	variants := 0
	for _, present := range []bool{t.waitID.Valid(), t.pause.valid(), t.output.Valid(), t.failure.Valid()} {
		if present {
			variants++
		}
	}
	switch t.advance {
	case TransitionKindContinue, TransitionKindCheckpoint:
		variants++
	case TransitionKindInvalid:
	default:
		return false
	}
	if variants != 1 {
		return false
	}
	return len(t.effects) == 0 || t.advance == TransitionKindContinue && validEffects(t.effects)
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
	wire := transitionWire{ConsumedSignals: t.consumedSignals}
	switch t.Kind() {
	case TransitionKindContinue:
		wire.Continue = new(t.effects)
		if t.effects == nil {
			wire.Continue = new([]Effect{})
		}
	case TransitionKindCheckpoint:
		wire.Checkpoint = &struct{}{}
	case TransitionKindWait:
		wire.Wait = &t.waitID
	case TransitionKindPause:
		wire.Pause = &t.pause.reason
	case TransitionKindComplete:
		wire.Complete = t.output.JSON()
	case TransitionKindFail:
		wire.Fail = &t.failure
	}
	return jsonv2.Marshal(wire)
}

func (t *Transition) UnmarshalJSON(data []byte) error {
	if t == nil {
		return fmt.Errorf("%w: nil receiver", ErrInvalidTransition)
	}
	wire, err := jsonwire.Decode[transitionWire](data, "consumed_signals")
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
	value := Transition{consumedSignals: wire.ConsumedSignals}
	members := 0
	if wire.Continue != nil {
		value.advance, value.effects = TransitionKindContinue, *wire.Continue
		members++
	}
	if wire.Checkpoint != nil {
		value.advance = TransitionKindCheckpoint
		members++
	}
	if wire.Wait != nil {
		value.waitID = *wire.Wait
		members++
	}
	if wire.Pause != nil {
		requested, err := parsePause(*wire.Pause)
		if err != nil {
			return Transition{}, fmt.Errorf("%w: %w", ErrInvalidTransition, err)
		}
		value.pause = requested
		members++
	}
	if len(wire.Complete) != 0 {
		output, err := ParsePayload(wire.Complete)
		if err != nil {
			return Transition{}, fmt.Errorf("%w: output: %w", ErrInvalidTransition, err)
		}
		value.output = output
		members++
	}
	if wire.Fail != nil {
		value.failure = *wire.Fail
		members++
	}
	if members != 1 || !value.Valid() {
		return Transition{}, fmt.Errorf("%w: exactly one valid variant is required", ErrInvalidTransition)
	}
	return value, nil
}

// transitionWire names its variant by the one member it carries.
type transitionWire struct {
	ConsumedSignals uint32          `json:"consumed_signals"`
	Continue        *[]Effect       `json:"continue,omitzero"`
	Checkpoint      *struct{}       `json:"checkpoint,omitzero"`
	Wait            *WaitID         `json:"wait,omitzero"`
	Pause           *string         `json:"pause,omitzero"`
	Complete        json.RawMessage `json:"complete,omitzero"`
	Fail            *Failure        `json:"fail,omitzero"`
}
