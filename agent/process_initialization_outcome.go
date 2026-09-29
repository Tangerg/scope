package agent

import (
	"context"
	"errors"
	"fmt"
	"time"
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
// persistence can still fail after an initialized outcome is accepted.
type ProcessInitializationOutcome struct {
	admission ProcessAdmission
	status    ProcessInitializationOutcomeStatus
	startedAt time.Time
	failure   Failure
}

func (p ProcessInitializationOutcome) Admission() ProcessAdmission { return p.admission }

func (p ProcessInitializationOutcome) Status() ProcessInitializationOutcomeStatus { return p.status }

// StartedAt returns the observed UTC lifecycle start time recorded before
// initialization. It is available only after successful initialization and does
// not identify the later Process publication boundary.
func (p ProcessInitializationOutcome) StartedAt() (time.Time, bool) {
	if p.status != ProcessInitializationOutcomeStatusInitialized || p.startedAt.IsZero() {
		return time.Time{}, false
	}
	return p.startedAt, true
}

func (p ProcessInitializationOutcome) Failure() (Failure, bool) {
	return p.failure, p.status == ProcessInitializationOutcomeStatusFailed
}

func (p ProcessInitializationOutcome) Valid() bool {
	if !p.admission.Valid() {
		return false
	}
	switch p.status {
	case ProcessInitializationOutcomeStatusInitialized:
		return !p.startedAt.IsZero() && p.startedAt.Location() == time.UTC && !p.failure.Valid()
	case ProcessInitializationOutcomeStatusFailed:
		return p.startedAt.IsZero() && p.failure.Valid()
	default:
		return false
	}
}

// ProcessInitializationOutcomeAcknowledger lets a Host close each accepted
// admission, even when initialization fails before a Process exists. Rejecting
// an initialized outcome prevents publication; accepting it does not guarantee
// later persistence. Initialization waits for this call, so implementations
// must be bounded, concurrency-safe, idempotent by admission identity, and must
// not re-enter the Engine. ctx keeps Host values without cancellation so an
// accepted admission can finish while its parent terminates; the Host applies
// its own deadline and reconciles an uncertain acknowledgment by admission
// identity. Restore produces no outcome.
type ProcessInitializationOutcomeAcknowledger interface {
	// AcknowledgeProcessInitializationOutcome runs before publication, so an
	// error rejects initialization without exposing a usable Process.
	AcknowledgeProcessInitializationOutcome(ctx context.Context, outcome ProcessInitializationOutcome) error
}

type ProcessInitializationOutcomeAcknowledgerFunc func(
	ctx context.Context,
	outcome ProcessInitializationOutcome,
) error

func (p ProcessInitializationOutcomeAcknowledgerFunc) AcknowledgeProcessInitializationOutcome(
	ctx context.Context,
	outcome ProcessInitializationOutcome,
) error {
	return p(ctx, outcome)
}

func initializedProcessOutcome(admission ProcessAdmission, startedAt time.Time) ProcessInitializationOutcome {
	return ProcessInitializationOutcome{
		admission: admission, status: ProcessInitializationOutcomeStatusInitialized,
		startedAt: startedAt.Round(0).UTC(),
	}
}

func failedProcessInitializationOutcome(admission ProcessAdmission, failure Failure) ProcessInitializationOutcome {
	return ProcessInitializationOutcome{
		admission: admission,
		status:    ProcessInitializationOutcomeStatusFailed,
		failure:   failure,
	}
}

func acknowledgeProcessInitializationOutcome(
	ctx context.Context,
	acknowledger ProcessInitializationOutcomeAcknowledger,
	outcome ProcessInitializationOutcome,
) (err error) {
	if acknowledger == nil {
		return nil
	}
	if !outcome.Valid() {
		return errors.New("invalid Process initialization outcome")
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			err = callbackPanic("ProcessInitializationOutcomeAcknowledger.AcknowledgeProcessInitializationOutcome", recovered)
		}
	}()
	defer func() { err = sealCallbackError(err) }()
	if err := acknowledger.AcknowledgeProcessInitializationOutcome(
		context.WithoutCancel(RequireContext(ctx)), outcome,
	); err != nil {
		return fmt.Errorf("agent: acknowledge Process initialization: %w", err)
	}
	return nil
}
