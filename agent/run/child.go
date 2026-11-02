package run

import (
	"errors"

	"github.com/Tangerg/scope/agent/internal/jsonwire"
)

var (
	ErrInvalidChildStart   = errors.New("run: invalid child process start")
	ErrInvalidChildControl = errors.New("run: invalid child control")
)

type ProgramResolver interface {
	Resolve(ProgramRef) (Program[Payload, Payload], error)
}

type ChildSpec struct {
	Key          ChildKey      `json:"key"`
	ProgramRef   ProgramRef    `json:"program_ref"`
	Input        Payload       `json:"input"`
	Budget       Budget        `json:"budget" jsonwire:"required"`
	Capabilities CapabilitySet `json:"capabilities" jsonwire:"required"`
}

func (c ChildSpec) Valid() bool { return c.Key.Valid() && c.ProgramRef.Valid() && c.Input.Valid() }
func (c *ChildSpec) UnmarshalJSON(data []byte) error {
	if c == nil {
		return ErrInvalidChildStart
	}
	type wire ChildSpec
	value, err := jsonwire.Decode[wire](data)
	if err != nil {
		return err
	}
	candidate := ChildSpec(value)
	if !candidate.Valid() {
		return ErrInvalidChildStart
	}
	*c = candidate
	return nil
}
func NewChildStartEffect(spec ChildSpec) (Effect, error) {
	if !spec.Valid() {
		return Effect{}, ErrInvalidChildStart
	}
	return Effect{spawn: &spec}, nil
}
func ParseChildStartEffect(effect Effect) (ChildSpec, error) {
	if effect.spawn == nil {
		return ChildSpec{}, ErrInvalidChildStart
	}
	return *effect.spawn, nil
}
func NewChildSignalEffect(child ProcessID, signal Signal) (Effect, error) {
	if !child.Valid() || !signal.Valid() {
		return Effect{}, ErrInvalidChildControl
	}
	return Effect{send: &childSignal{Child: child, Signal: signal}}, nil
}
func NewChildCancelEffect(child ProcessID, reason string) (Effect, error) {
	if !child.Valid() || validateTerminationReason(reason) != nil {
		return Effect{}, ErrInvalidChildControl
	}
	return Effect{cancel: &childCancellation{Child: child, Reason: reason}}, nil
}

type childSignal struct {
	Child  ProcessID `json:"child"`
	Signal Signal    `json:"signal"`
}
type childCancellation struct {
	Child  ProcessID `json:"child"`
	Reason string    `json:"reason"`
}
