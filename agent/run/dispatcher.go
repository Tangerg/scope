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
	return e.Replay == other.Replay && e.RequiredCapabilities.Equal(other.RequiredCapabilities)
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

// Dispatcher performs the dispatcher Effects its Program's Steps declare. The
// Runtime calls Dispatch with a live context and a valid EffectRequest for an
// Effect its Program's Execution declared, so a Dispatcher checks only that
// the Effect payload belongs to its own protocol. The Effect's policy was
// committed with the Step; a Dispatcher cannot change it.
//
// Dispatch must distinguish a definite result from interruption: an error
// leaves the outcome unknown. A work context canceled by local writer shutdown
// has ErrWriterStopped as its cause; this is not a durable business failure.
type Dispatcher interface {
	Dispatch(ctx context.Context, request EffectRequest, emit DeltaEmitter) (Settlement, error)
}

type DispatcherFunc func(ctx context.Context, request EffectRequest, emit DeltaEmitter) (Settlement, error)

func (d DispatcherFunc) Dispatch(ctx context.Context, request EffectRequest, emit DeltaEmitter) (Settlement, error) {
	return d(ctx, request, emit)
}

// DispatchMiddleware decorates every dispatch a Runtime performs; see
// Config.DispatchMiddlewares. It runs inside the Effect, so it may perform
// I/O, annotate the context, or settle without calling next, as a cache does.
// The Runtime validates what it returns exactly as it validates a
// Dispatcher's. A dispatch repeated under ReplayPolicySameIdentity runs it
// again, so observing middleware tolerates repeated calls for one EffectID.
type DispatchMiddleware func(next Dispatcher) Dispatcher

// decorate composes middlewares around dispatcher, the first outermost; nil
// entries are skipped, as in every Scope composition.
func decorate(dispatcher Dispatcher, middlewares []DispatchMiddleware) Dispatcher {
	for _, middleware := range slices.Backward(middlewares) {
		if middleware != nil {
			dispatcher = middleware(dispatcher)
		}
	}
	return dispatcher
}
