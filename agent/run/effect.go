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
	call   *dispatcherCall
	start  *ChildSpec
	signal *childSignal
	cancel *childCancellation
}

// dispatcherCall is an external operation together with the replay policy its
// creator guarantees. The policy travels with the operation so that the Step
// declaring it is its only owner: replay compares it, and nothing at dispatch
// time can supply or change it.
type dispatcherCall struct {
	Payload Payload      `json:"payload"`
	Policy  EffectPolicy `json:"policy"`
}

func (d dispatcherCall) equal(other dispatcherCall) bool {
	return d.Payload.Equal(other.Payload) && d.Policy.equal(other.Policy)
}

// NewDispatcherEffect declares an external operation for the Program's
// Dispatcher together with the replay policy and capabilities it requires.
func NewDispatcherEffect(payload Payload, policy EffectPolicy) (Effect, error) {
	if !payload.Valid() {
		return Effect{}, fmt.Errorf("%w: a dispatcher Effect requires a payload", ErrInvalidPayload)
	}
	if !policy.Valid() {
		return Effect{}, fmt.Errorf("%w: a dispatcher Effect requires a valid policy", ErrInvalidEffect)
	}
	return Effect{call: &dispatcherCall{Payload: payload, Policy: policy}}, nil
}

func (e Effect) Target() EffectTarget {
	if e.call != nil {
		return EffectTargetDispatcher
	}
	if e.Valid() {
		return EffectTargetFramework
	}
	return EffectTargetInvalid
}

// Policy returns the policy a dispatcher Effect declared. Framework Effects
// are the kernel's own and always repeat under their identity.
func (e Effect) Policy() EffectPolicy {
	switch e.Target() {
	case EffectTargetDispatcher:
		return e.call.Policy
	case EffectTargetFramework:
		return EffectPolicy{Replay: ReplayPolicySameIdentity}
	default:
		return EffectPolicy{}
	}
}

func (e Effect) Payload() Payload {
	if e.call == nil {
		return Payload{}
	}
	return e.call.Payload
}

// ChildStart reports the child a framework Effect starts.
func (e Effect) ChildStart() (ChildSpec, bool) {
	if e.start == nil {
		return ChildSpec{}, false
	}
	return *e.start, true
}

// Valid rejects the zero Effect. Every constructor admits exactly one valid
// variant and the decoder admits only wellFormed values, so presence is enough.
func (e Effect) Valid() bool {
	return e.call != nil || e.start != nil || e.signal != nil || e.cancel != nil
}

func (e Effect) wellFormed() bool {
	count := 0
	if e.call != nil {
		if !e.call.Payload.Valid() || !e.call.Policy.Valid() {
			return false
		}
		count++
	}
	if e.start != nil {
		if !e.start.Valid() {
			return false
		}
		count++
	}
	if e.signal != nil {
		if !e.signal.Child.Valid() || !e.signal.Signal.Valid() {
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
	return jsonv2.Marshal(effectWire{Call: e.call, ChildStart: e.start, ChildSignal: e.signal, ChildCancel: e.cancel})
}

func (e *Effect) UnmarshalJSON(data []byte) error {
	if e == nil {
		return ErrInvalidEffect
	}
	wire, err := jsonwire.Decode[effectWire](data)
	if err != nil {
		return err
	}
	value := Effect{call: wire.Call, start: wire.ChildStart, signal: wire.ChildSignal, cancel: wire.ChildCancel}
	if !value.wellFormed() {
		return ErrInvalidEffect
	}
	*e = value
	return nil
}

// Equal reports whether both Effects declare the same operation. An invalid
// Effect equals nothing, so it is never evidence of a committed one. Each
// variant's value objects own their equality, so comparison reads normalized
// facts instead of re-encoding payloads that grow with history.
func (e Effect) Equal(other Effect) bool {
	if !e.Valid() || !other.Valid() {
		return false
	}
	switch {
	case e.call != nil:
		return other.call != nil && e.call.equal(*other.call)
	case e.start != nil:
		return other.start != nil && e.start.Equal(*other.start)
	case e.signal != nil:
		return other.signal != nil && e.signal.equal(*other.signal)
	default:
		return other.cancel != nil && *e.cancel == *other.cancel
	}
}

type effectWire struct {
	Call        *dispatcherCall    `json:"call,omitzero"`
	ChildStart  *ChildSpec         `json:"child_start,omitzero"`
	ChildSignal *childSignal       `json:"child_signal,omitzero"`
	ChildCancel *childCancellation `json:"child_cancel,omitzero"`
}
