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

func (e Effect) equal(other Effect) bool {
	left, err := jsonv2.Marshal(e)
	if err != nil {
		return false
	}
	right, err := jsonv2.Marshal(other)
	return err == nil && string(left) == string(right)
}

type effectWire struct {
	Call   Payload            `json:"call,omitzero"`
	Spawn  *ChildSpec         `json:"spawn,omitzero"`
	Send   *childSignal       `json:"send,omitzero"`
	Cancel *childCancellation `json:"cancel,omitzero"`
}
