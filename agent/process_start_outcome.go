package agent

import (
	"context"
	"errors"
	"fmt"
	"time"
)

type ProcessStartOutcomeStatus string

const (
	ProcessStartOutcomeStatusInvalid ProcessStartOutcomeStatus = ""
	ProcessStartOutcomeStatusStarted ProcessStartOutcomeStatus = "started"
	ProcessStartOutcomeStatusAborted ProcessStartOutcomeStatus = "aborted"
)

func (p ProcessStartOutcomeStatus) Valid() bool {
	return p == ProcessStartOutcomeStatusStarted || p == ProcessStartOutcomeStatusAborted
}

func (p ProcessStartOutcomeStatus) String() string {
	if !p.Valid() {
		return invalidEnumName
	}
	return string(p)
}

// ProcessStartOutcome separates initialization acceptance from publication:
// persistence can still fail after a started outcome is accepted.
type ProcessStartOutcome struct {
	admission ProcessAdmission
	status    ProcessStartOutcomeStatus
	startedAt time.Time
	failure   Failure
}

func (p ProcessStartOutcome) Admission() ProcessAdmission { return p.admission }

func (p ProcessStartOutcome) Status() ProcessStartOutcomeStatus { return p.status }

func (p ProcessStartOutcome) StartedAt() (time.Time, bool) {
	if p.status != ProcessStartOutcomeStatusStarted || p.startedAt.IsZero() {
		return time.Time{}, false
	}
	return p.startedAt, true
}

func (p ProcessStartOutcome) Failure() (Failure, bool) {
	return p.failure, p.status == ProcessStartOutcomeStatusAborted
}

func (p ProcessStartOutcome) Valid() bool {
	if !p.admission.Valid() {
		return false
	}
	switch p.status {
	case ProcessStartOutcomeStatusStarted:
		return !p.startedAt.IsZero() && p.startedAt.Location() == time.UTC && !p.failure.Valid()
	case ProcessStartOutcomeStatusAborted:
		return p.startedAt.IsZero() && p.failure.Valid()
	default:
		return false
	}
}

// ProcessStartOutcomeAcknowledger lets a Host close each accepted admission even
// when initialization fails before a Process exists. Rejecting a started outcome
// prevents publication; accepting it does not guarantee later persistence.
// Implementations must be bounded, concurrency-safe, idempotent by admission
// identity, and must not re-enter Engine or Process, because initialization waits
// for this call. Restore produces no outcome because it does not initialize.
// ctx retains Host values but excludes cancellation so an accepted admission
// can finish its acknowledgment even when its parent is terminating.
type ProcessStartOutcomeAcknowledger interface {
	// AcknowledgeProcessStartOutcome must return before publication so a Host
	// can reject initialization without exposing a usable Process.
	AcknowledgeProcessStartOutcome(ctx context.Context, outcome ProcessStartOutcome) error
}

type ProcessStartOutcomeAcknowledgerFunc func(
	ctx context.Context,
	outcome ProcessStartOutcome,
) error

func (p ProcessStartOutcomeAcknowledgerFunc) AcknowledgeProcessStartOutcome(
	ctx context.Context,
	outcome ProcessStartOutcome,
) error {
	return p(ctx, outcome)
}

func startedProcessOutcome(admission ProcessAdmission, startedAt time.Time) ProcessStartOutcome {
	return ProcessStartOutcome{
		admission: admission, status: ProcessStartOutcomeStatusStarted,
		startedAt: startedAt.Round(0).UTC(),
	}
}

func abortedProcessOutcome(admission ProcessAdmission, failure Failure) ProcessStartOutcome {
	return ProcessStartOutcome{
		admission: admission,
		status:    ProcessStartOutcomeStatusAborted,
		failure:   failure,
	}
}

func acknowledgeProcessStartOutcome(
	ctx context.Context,
	acknowledger ProcessStartOutcomeAcknowledger,
	outcome ProcessStartOutcome,
) (err error) {
	if acknowledger == nil {
		return nil
	}
	if !outcome.Valid() {
		return errors.New("invalid Process start outcome")
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("process-start outcome acknowledger panicked: %v", recovered)
		}
	}()
	if err := acknowledger.AcknowledgeProcessStartOutcome(
		context.WithoutCancel(requireContext(ctx)), outcome,
	); err != nil {
		return fmt.Errorf("agent: acknowledge Process initialization: %w", err)
	}
	return nil
}
