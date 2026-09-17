package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/Tangerg/scope/agent/internal/jsonwire"
)

const maxPauseReasonBytes = 4096

var ErrInvalidTransition = errors.New("agent: invalid transition")

// TransitionKind is the lifecycle intent produced by one bounded Step.
type TransitionKind string

const (
	// TransitionKindInvalid is the invalid zero value.
	TransitionKindInvalid TransitionKind = ""
	// TransitionKindContinue advances to another runnable Step.
	TransitionKindContinue TransitionKind = "continue"
	// TransitionKindCheckpoint persists state before another Step can run.
	TransitionKindCheckpoint TransitionKind = "checkpoint"
	// TransitionKindWait enters an Engine-minted wait.
	TransitionKindWait TransitionKind = "wait"
	// TransitionKindPause enters an explicit scheduling pause.
	TransitionKindPause TransitionKind = "pause"
	// TransitionKindComplete commits a validated semantic Output.
	TransitionKindComplete TransitionKind = "complete"
	// TransitionKindFail commits a classified failure.
	TransitionKindFail TransitionKind = "fail"
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
	reason          string
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

// Checkpoint commits the consumed Signal prefix and candidate state before any
// further Step or Effect in this Process can run. In durable mode TreeDurability
// must acknowledge the complete tree first. In ephemeral mode it advances without
// a storage claim.
// It performs no external operation and creates no settlement Signal.
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

// Pause requests an explicit scheduling pause with a bounded diagnostic reason.
func Pause(consumedSignals uint32, reason string) (Transition, error) {
	if !validPauseReason(reason) {
		return Transition{}, fmt.Errorf("%w: pause reason must be non-empty, trimmed UTF-8, and at most %d bytes", ErrInvalidTransition, maxPauseReasonBytes)
	}
	return Transition{kind: TransitionKindPause, consumedSignals: consumedSignals, reason: reason}, nil
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

// Kind returns the requested lifecycle intent.
func (t Transition) Kind() TransitionKind { return t.kind }

// ConsumedSignals returns the length of the delivered Signal prefix to commit.
func (t Transition) ConsumedSignals() uint32 { return t.consumedSignals }

// Effects returns independently owned operation intents in declaration order.
func (t Transition) Effects() []Effect { return cloneEffectsUnchecked(t.effects) }

// WaitID returns the wait target for a Wait transition.
func (t Transition) WaitID() (WaitID, bool) { return t.waitID, t.kind == TransitionKindWait }

// Reason returns the pause reason for a Pause transition.
func (t Transition) Reason() (string, bool) { return t.reason, t.kind == TransitionKindPause }

// Output returns the final result for a Complete transition.
func (t Transition) Output() (Payload, bool) { return t.output, t.kind == TransitionKindComplete }

// Failure returns the terminal failure for a Fail transition.
func (t Transition) Failure() (Failure, bool) { return t.failure, t.kind == TransitionKindFail }

func (t Transition) Valid() bool {
	switch t.kind {
	case TransitionKindContinue:
		return validEffects(t.effects) && !t.waitID.Valid() && t.reason == "" && !t.output.Valid() && !t.failure.Valid()
	case TransitionKindCheckpoint:
		return len(t.effects) == 0 && !t.waitID.Valid() && t.reason == "" && !t.output.Valid() && !t.failure.Valid()
	case TransitionKindWait:
		return len(t.effects) == 0 && t.waitID.Valid() && t.reason == "" && !t.output.Valid() && !t.failure.Valid()
	case TransitionKindPause:
		return len(t.effects) == 0 && !t.waitID.Valid() && validPauseReason(t.reason) && !t.output.Valid() && !t.failure.Valid()
	case TransitionKindComplete:
		return len(t.effects) == 0 && !t.waitID.Valid() && t.reason == "" && t.output.Valid() && !t.failure.Valid()
	case TransitionKindFail:
		return len(t.effects) == 0 && !t.waitID.Valid() && t.reason == "" && !t.output.Valid() && t.failure.Valid()
	default:
		return false
	}
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
		wire.Reason = t.reason
	case TransitionKindComplete:
		wire.Output = t.output.JSON()
	case TransitionKindFail:
		wire.Failure = &t.failure
	}
	return json.Marshal(wire)
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
	value := Transition{kind: wire.Kind, consumedSignals: wire.ConsumedSignals,
		effects: wire.Effects, reason: wire.Reason}
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

func validPauseReason(reason string) bool {
	return reason != "" && utf8.ValidString(reason) && strings.TrimSpace(reason) == reason && len(reason) <= maxPauseReasonBytes
}

type transitionWire struct {
	Kind            TransitionKind  `json:"kind"`
	ConsumedSignals uint32          `json:"consumed_signals"`
	Effects         []Effect        `json:"effects,omitempty"`
	WaitID          *WaitID         `json:"wait_id,omitempty"`
	Reason          string          `json:"reason,omitempty"`
	Output          json.RawMessage `json:"output,omitempty"`
	Failure         *Failure        `json:"failure,omitempty"`
}
