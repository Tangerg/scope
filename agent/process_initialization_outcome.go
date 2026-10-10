package agent

import (
	"context"
	"errors"
	"fmt"
)

type ProcessInitializationOutcomeStatus string

const (
	ProcessInitializationOutcomeStatusInvalid     ProcessInitializationOutcomeStatus = ""
	ProcessInitializationOutcomeStatusInitialized ProcessInitializationOutcomeStatus = "initialized"
	ProcessInitializationOutcomeStatusFailed      ProcessInitializationOutcomeStatus = "failed"
)

func (p ProcessInitializationOutcomeStatus) Valid() bool {
	switch p {
	case ProcessInitializationOutcomeStatusInitialized, ProcessInitializationOutcomeStatusFailed:
		return true
	default:
		return false
	}
}

func (p ProcessInitializationOutcomeStatus) String() string {
	if !p.Valid() {
		return invalidEnumName
	}
	return string(p)
}

// ProcessInitializationOutcome separates initialization acceptance from publication:
// persistence can still fail after an initialized outcome is accepted. It carries
// only the admission result, never a timestamp: the Process start time is a
// publication fact owned by the Process snapshot, so the outcome stays identical
// across any recovery replay of one admission.
type ProcessInitializationOutcome struct {
	admission ProcessAdmission
	failure   Failure
}

func (p ProcessInitializationOutcome) Admission() ProcessAdmission { return p.admission }

// Status is determined by whether the outcome carries a failure.
func (p ProcessInitializationOutcome) Status() ProcessInitializationOutcomeStatus {
	switch {
	case !p.admission.Valid():
		return ProcessInitializationOutcomeStatusInvalid
	case p.failure.Valid():
		return ProcessInitializationOutcomeStatusFailed
	default:
		return ProcessInitializationOutcomeStatusInitialized
	}
}

func (p ProcessInitializationOutcome) Failure() (Failure, bool) {
	return p.failure, p.Status() == ProcessInitializationOutcomeStatusFailed
}

func (p ProcessInitializationOutcome) Valid() bool {
	return p.Status() != ProcessInitializationOutcomeStatusInvalid
}

// ProcessInitializationAcknowledger lets a Host close each accepted
// admission, even when initialization fails before a Process exists. Rejecting
// an initialized outcome prevents publication; accepting it does not guarantee
// later persistence. Initialization waits for this call, so implementations
// must be bounded, concurrency-safe, idempotent by admission identity, and must
// not re-enter the Engine. ctx keeps Host values without cancellation so an
// accepted admission can finish while its parent terminates; the Host applies
// its own deadline and reconciles an uncertain acknowledgment by admission
// identity. Restore produces no outcome.
type ProcessInitializationAcknowledger interface {
	// Acknowledge runs before publication, so an
	// error rejects initialization without exposing a usable Process.
	Acknowledge(ctx context.Context, outcome ProcessInitializationOutcome) error
}

type ProcessInitializationAcknowledgerFunc func(
	ctx context.Context,
	outcome ProcessInitializationOutcome,
) error

func (p ProcessInitializationAcknowledgerFunc) Acknowledge(
	ctx context.Context,
	outcome ProcessInitializationOutcome,
) error {
	return p(ctx, outcome)
}

func initializedProcessOutcome(admission ProcessAdmission) ProcessInitializationOutcome {
	return ProcessInitializationOutcome{admission: admission}
}

func failedProcessInitializationOutcome(admission ProcessAdmission, failure Failure) ProcessInitializationOutcome {
	return ProcessInitializationOutcome{admission: admission, failure: failure}
}

func acknowledgeProcessInitialization(
	ctx context.Context,
	acknowledger ProcessInitializationAcknowledger,
	outcome ProcessInitializationOutcome,
) error {
	if acknowledger == nil {
		return nil
	}
	if !outcome.Valid() {
		return errors.New("invalid Process initialization outcome")
	}
	err := invokeCallbackErr("ProcessInitializationAcknowledger.Acknowledge", func() error {
		return acknowledger.Acknowledge(context.WithoutCancel(RequireContext(ctx)), outcome)
	})
	if err != nil {
		return fmt.Errorf("agent: acknowledge Process initialization: %w", err)
	}
	return nil
}
