package run

import (
	"context"
	"slices"

	"github.com/Tangerg/scope/agent/internal/enum"
	"github.com/Tangerg/scope/agent/internal/jsonwire"
)

type ReplayPolicy string

const (
	ReplayPolicyInvalid      ReplayPolicy = ""
	ReplayPolicyNever        ReplayPolicy = "never"
	ReplayPolicySameIdentity ReplayPolicy = "same_identity"
)

func (r ReplayPolicy) Valid() bool { return r == ReplayPolicyNever || r == ReplayPolicySameIdentity }

func (r ReplayPolicy) String() string { return enum.String(r) }

type EffectPolicy struct {
	Replay               ReplayPolicy  `json:"replay"`
	RequiredCapabilities CapabilitySet `json:"required_capabilities" jsonwire:"required"`
}

func (e EffectPolicy) Valid() bool { return e.Replay.Valid() }

func (e EffectPolicy) equal(other EffectPolicy) bool {
	return e.Replay == other.Replay && slices.Equal(e.RequiredCapabilities.values, other.RequiredCapabilities.values)
}

func (e *EffectPolicy) UnmarshalJSON(data []byte) error {
	if e == nil {
		return ErrInvalidEffect
	}
	type wire EffectPolicy
	value, err := jsonwire.Decode[wire](data)
	if err != nil {
		return err
	}
	candidate := EffectPolicy(value)
	if !candidate.Valid() {
		return ErrInvalidEffect
	}
	*e = candidate
	return nil
}

type EffectRequest struct {
	id      EffectID
	epoch   Epoch
	program ProgramRef
	effect  Effect
}

func (e EffectRequest) ID() EffectID           { return e.id }
func (e EffectRequest) Epoch() Epoch           { return e.epoch }
func (e EffectRequest) ProgramRef() ProgramRef { return e.program }
func (e EffectRequest) Effect() Effect         { return e.effect }

func (e EffectRequest) Valid() bool {
	return e.id.Valid() && e.epoch > 0 && e.program.Valid() && e.effect.Valid()
}

// DeltaEmitter publishes one observational increment of the Effect being
// dispatched. The Runtime always supplies one; increments no Host consumes are
// discarded, so a Dispatcher never checks whether anyone listens.
type DeltaEmitter func(Payload)

func discardDelta(Payload) {}

// Policy is captured in EffectIntent before dispatch. Recovery uses that
// acknowledged declaration, never a newly queried replay decision.
// The Runtime calls Policy with a valid dispatcher Effect, and Dispatch with a
// live context and a valid EffectRequest, for an Effect its Program's
// Execution declared, so a Dispatcher checks only that the Effect payload
// belongs to its own protocol.
// Dispatch must distinguish a definite result from interruption: an error
// leaves the outcome unknown. A work context canceled by local writer shutdown
// has ErrWriterStopped as its cause; this is not a durable business failure.
type Dispatcher interface {
	Dispatch(context.Context, EffectRequest, DeltaEmitter) (Settlement, error)
	Policy(Effect) EffectPolicy
}
