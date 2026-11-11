package run

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"

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

func (e EffectTarget) String() string {
	if !e.Valid() {
		return invalidEnumName
	}
	return string(e)
}

// Each variant owns its operation; a payload is never inspected to guess a
// framework operation or to distinguish a completion from external input.
type Effect struct {
	call   Payload
	spawn  *ChildSpec
	send   *childSignal
	cancel *childCancellation
}

func NewDispatcherEffect(payload json.RawMessage) (Effect, error) {
	value, err := ParsePayload(payload)
	if err != nil {
		return Effect{}, err
	}
	return Effect{call: value}, nil
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

func (e Effect) Valid() bool {
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

func (e Effect) clone() Effect {
	owned := e
	if e.spawn != nil {
		v := *e.spawn
		owned.spawn = &v
	}
	if e.send != nil {
		v := *e.send
		owned.send = &v
	}
	if e.cancel != nil {
		v := *e.cancel
		owned.cancel = &v
	}
	return owned
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
	if !value.Valid() {
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
