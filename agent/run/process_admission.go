package run

import (
	"context"
	"errors"
	"fmt"
)

var ErrProcessAdmissionRejected = errors.New("run: process admission rejected")

type ProcessAdmission struct {
	start        StartID
	process      ProcessID
	program      ProgramRef
	descriptor   Descriptor
	budget       Budget
	capabilities CapabilitySet
}

func (p ProcessAdmission) StartID() (StartID, bool)    { return p.start, p.start.wellFormed() }
func (p ProcessAdmission) ProcessID() ProcessID        { return p.process }
func (p ProcessAdmission) ProgramRef() ProgramRef      { return p.program }
func (p ProcessAdmission) Descriptor() Descriptor      { return p.descriptor }
func (p ProcessAdmission) Budget() Budget              { return p.budget }
func (p ProcessAdmission) Capabilities() CapabilitySet { return p.capabilities }
func (p ProcessAdmission) Valid() bool {
	return p.process.Valid() && p.program.Valid() && p.descriptor.Valid()
}

type ProcessAdmitter interface {
	Admit(context.Context, ProcessAdmission) error
}

type ProcessAdmitterFunc func(context.Context, ProcessAdmission) error

func (p ProcessAdmitterFunc) Admit(ctx context.Context, admission ProcessAdmission) error {
	return p(ctx, admission)
}

func requestProcessAdmission(ctx context.Context, admitter ProcessAdmitter, admission ProcessAdmission) error {
	if admitter == nil {
		return nil
	}
	if !admission.Valid() {
		return ErrProcessAdmissionRejected
	}
	err := invokeCallbackErr("ProcessAdmitter.Admit", func() error { return admitter.Admit(ctx, admission) })
	if err != nil {
		return fmt.Errorf("%w: %w", ErrProcessAdmissionRejected, err)
	}
	return nil
}
