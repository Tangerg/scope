package run

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"

	"github.com/Tangerg/scope/agent/internal/enum"
	"github.com/Tangerg/scope/agent/internal/jsonwire"
)

var ErrInvalidEffect = errors.New("run: invalid effect")

type EffectTarget string

const (
	EffectTargetInvalid    EffectTarget = ""
	EffectTargetFramework  EffectTarget = "framework"
	EffectTargetDispatcher EffectTarget = "dispatcher"
)

func (e EffectTarget) Valid() bool { return e == EffectTargetFramework || e == EffectTargetDispatcher }

func (e EffectTarget) String() string { return enum.String(e) }

// Each variant owns its operation; a payload is never inspected to guess a
// framework operation or to distinguish a completion from external input.
// Variants are reached only through pointers nothing writes after
// construction, so copies of an Effect share them safely.
type Effect struct {
	call   Payload
	spawn  *ChildSpec
	send   *childSignal
	cancel *childCancellation
}

func NewDispatcherEffect(payload Payload) (Effect, error) {
	if !payload.Valid() {
		return Effect{}, fmt.Errorf("%w: a dispatcher Effect requires a payload", ErrInvalidPayload)
	}
	return Effect{call: payload}, nil
}

func (e Effect) Target() EffectTarget {
	if e.call.Valid() {
		return EffectTargetDispatcher
	}
	if e.Valid() {
		return EffectTargetFramework
	}
	return EffectTargetInvalid
}

func (e Effect) intrinsicPolicy() (EffectPolicy, bool) {
	if e.Target() != EffectTargetFramework {
		return EffectPolicy{}, false
	}
	return EffectPolicy{Replay: ReplayPolicySameIdentity}, true
}

func (e Effect) Payload() Payload { return e.call }

// ChildStart reports the child a framework Effect starts.
func (e Effect) ChildStart() (ChildSpec, bool) {
	if e.spawn == nil {
		return ChildSpec{}, false
	}
	return *e.spawn, true
}

// Valid rejects the zero Effect. Every constructor admits exactly one valid
// variant and the decoder admits only wellFormed values, so presence is enough.
func (e Effect) Valid() bool {
	return e.call.Valid() || e.spawn != nil || e.send != nil || e.cancel != nil
}

func (e Effect) wellFormed() bool {
	count := 0
	if e.call.Valid() {
		count++
	}
	if e.spawn != nil {
		if !e.spawn.Valid() {
			return false
		}
		count++
	}
	if e.send != nil {
		if !e.send.Child.Valid() || !e.send.Signal.Valid() {
			return false
		}
		count++
	}
	if e.cancel != nil {
		if !e.cancel.Child.Valid() || validateTerminationReason(e.cancel.Reason) != nil {
			return false
		}
		count++
	}
	return count == 1
}

func (e Effect) MarshalJSON() ([]byte, error) {
	if !e.Valid() {
		return nil, ErrInvalidEffect
	}
	return jsonv2.Marshal(effectWire{Call: e.call, Spawn: e.spawn, Send: e.send, Cancel: e.cancel})
}

func (e *Effect) UnmarshalJSON(data []byte) error {
	if e == nil {
		return ErrInvalidEffect
	}
	wire, err := jsonwire.Decode[effectWire](data)
	if err != nil {
		return err
	}
	value := Effect{call: wire.Call, spawn: wire.Spawn, send: wire.Send, cancel: wire.Cancel}
	if !value.wellFormed() {
		return ErrInvalidEffect
	}
	*e = value
	return nil
}

// equal never treats an invalid Effect as evidence of a committed one. Each
// variant's value objects own their equality, so log validation compares
// normalized facts instead of re-encoding payloads that grow with history.
func (e Effect) equal(other Effect) bool {
	if !e.Valid() || !other.Valid() {
		return false
	}
	switch {
	case e.call.Valid():
		return e.call.Equal(other.call)
	case e.spawn != nil:
		return other.spawn != nil && e.spawn.equal(*other.spawn)
	case e.send != nil:
		return other.send != nil && e.send.equal(*other.send)
	default:
		return other.cancel != nil && *e.cancel == *other.cancel
	}
}

type effectWire struct {
	Call   Payload            `json:"call,omitzero"`
	Spawn  *ChildSpec         `json:"spawn,omitzero"`
	Send   *childSignal       `json:"send,omitzero"`
	Cancel *childCancellation `json:"cancel,omitzero"`
}
