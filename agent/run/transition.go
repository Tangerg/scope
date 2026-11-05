package run

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"slices"

	"github.com/Tangerg/scope/agent/internal/jsonwire"
)

var ErrInvalidTransition = errors.New("run: invalid transition")

type TransitionKind string

const (
	TransitionKindInvalid  TransitionKind = ""
	TransitionKindContinue TransitionKind = "continue"
	TransitionKindYield    TransitionKind = "yield"
	TransitionKindComplete TransitionKind = "complete"
	TransitionKindFail     TransitionKind = "fail"
)

func (t TransitionKind) Valid() bool {
	switch t {
	case TransitionKindContinue, TransitionKindYield, TransitionKindComplete, TransitionKindFail:
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

type Transition struct {
	kind     TransitionKind
	consumed uint32
	effects  []Effect
	waits    []Wait
	output   Payload
	failure  Failure
}

func Continue(consumed uint32, effects ...Effect) (Transition, error) {
	t := Transition{kind: TransitionKindContinue, consumed: consumed, effects: cloneEffects(effects)}
	if !t.Valid() {
		return Transition{}, ErrInvalidTransition
	}
	return t, nil
}

func Yield(consumed uint32, effects []Effect, waits ...Wait) (Transition, error) {
	t := Transition{kind: TransitionKindYield, consumed: consumed, effects: cloneEffects(effects), waits: slices.Clone(waits)}
	if !t.Valid() {
		return Transition{}, ErrInvalidTransition
	}
	return t, nil
}

func Complete(consumed uint32, output Payload) (Transition, error) {
	t := Transition{kind: TransitionKindComplete, consumed: consumed, output: output}
	if !t.Valid() {
		return Transition{}, ErrInvalidTransition
	}
	return t, nil
}

func Fail(consumed uint32, failure Failure) (Transition, error) {
	t := Transition{kind: TransitionKindFail, consumed: consumed, failure: failure}
	if !t.Valid() {
		return Transition{}, ErrInvalidTransition
	}
	return t, nil
}

func (t Transition) Kind() TransitionKind     { return t.kind }
func (t Transition) Consumed() uint32         { return t.consumed }
func (t Transition) Effects() []Effect        { return cloneEffects(t.effects) }
func (t Transition) Waits() []Wait            { return slices.Clone(t.waits) }
func (t Transition) Output() (Payload, bool)  { return t.output, t.output.Valid() }
func (t Transition) Failure() (Failure, bool) { return t.failure, t.failure.Valid() }
func (t Transition) Valid() bool {
	for _, effect := range t.effects {
		if !effect.Valid() {
			return false
		}
	}
	keys := make(map[WaitKey]struct{}, len(t.waits))
	for _, wait := range t.waits {
		if !wait.Valid() {
			return false
		}
		if _, ok := keys[wait.Key]; ok {
			return false
		}
		keys[wait.Key] = struct{}{}
	}
	switch t.kind {
	case TransitionKindContinue:
		return len(t.waits) == 0 && !t.output.Valid() && !t.failure.Valid()
	case TransitionKindYield:
		return !t.output.Valid() && !t.failure.Valid()
	case TransitionKindComplete:
		return t.output.Valid() && !t.failure.Valid() && len(t.effects) == 0 && len(t.waits) == 0
	case TransitionKindFail:
		return t.failure.Valid() && !t.output.Valid() && len(t.effects) == 0 && len(t.waits) == 0
	default:
		return false
	}
}

func (t Transition) MarshalJSON() ([]byte, error) {
	if !t.Valid() {
		return nil, ErrInvalidTransition
	}
	wire := transitionWire{Kind: t.kind, Consumed: t.consumed, Effects: t.effects, Waits: t.waits, Output: t.output}
	if t.failure.Valid() {
		wire.Failure = &t.failure
	}
	return jsonv2.Marshal(wire)
}

func (t *Transition) UnmarshalJSON(data []byte) error {
	if t == nil {
		return ErrInvalidTransition
	}
	wire, err := jsonwire.Decode[transitionWire](data)
	if err != nil {
		return err
	}
	value := Transition{kind: wire.Kind, consumed: wire.Consumed, effects: wire.Effects, waits: wire.Waits, output: wire.Output}
	if wire.Failure != nil {
		value.failure = *wire.Failure
	}
	if !value.Valid() {
		return ErrInvalidTransition
	}
	*t = value
	return nil
}

// equal compares the committed facts the wire form encodes; absent and empty
// effect or wait lists encode identically and are therefore the same.
func (t Transition) equal(other Transition) bool {
	return t.Valid() && other.Valid() && t.kind == other.kind && t.consumed == other.consumed &&
		slices.EqualFunc(t.effects, other.effects, Effect.equal) &&
		slices.EqualFunc(t.waits, other.waits, Wait.equal) &&
		t.output.Equal(other.output) && t.failure == other.failure
}

func cloneEffects(effects []Effect) []Effect {
	owned := slices.Clone(effects)
	for index := range owned {
		owned[index] = owned[index].clone()
	}
	return owned
}

type transitionWire struct {
	Kind     TransitionKind `json:"kind"`
	Consumed uint32         `json:"consumed" jsonwire:"required"`
	Effects  []Effect       `json:"effects,omitempty"`
	Waits    []Wait         `json:"waits,omitempty"`
	Output   Payload        `json:"output,omitzero"`
	Failure  *Failure       `json:"failure,omitzero"`
}
