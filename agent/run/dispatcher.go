package run

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/Tangerg/scope/agent/internal/jsonwire"
)

type ReplayPolicy string

const (
	ReplayPolicyInvalid      ReplayPolicy = ""
	ReplayPolicyNever        ReplayPolicy = "never"
	ReplayPolicySameIdentity ReplayPolicy = "same_identity"
)

func (r ReplayPolicy) Valid() bool { return r == ReplayPolicyNever || r == ReplayPolicySameIdentity }
func (r ReplayPolicy) String() string {
	if !r.Valid() {
		return invalidEnumName
	}
	return string(r)
}

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
func (e EffectRequest) Effect() Effect         { return e.effect.clone() }
func (e EffectRequest) Valid() bool {
	return e.id.Valid() && e.epoch > 0 && e.program.Valid() && e.effect.Valid()
}

type DeltaEmitter func(json.RawMessage)

// Policy is captured in EffectIntent before dispatch. Recovery uses that
// acknowledged declaration, never a newly queried replay decision.
// Dispatch must distinguish a definite result from interruption: an error
// leaves the outcome unknown. A work context canceled by local writer shutdown
// has ErrWriterStopped as its cause; this is not a durable business failure.
type Dispatcher interface {
	Dispatch(context.Context, EffectRequest, DeltaEmitter) (Settlement, error)
	Policy(Effect) EffectPolicy
}
