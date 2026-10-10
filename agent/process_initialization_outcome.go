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
// the admission result and the incarnation of the physical attempt that produced
// it, never a timestamp: the Process start time is a publication fact owned by the
// Process snapshot. A child whose attempt is interrupted before its result
// persists is re-attempted after recovery under a new incarnation, so one
// admission can produce more than one outcome; each is unique by its
// (admission, incarnation) pair and the published tree identifies the live one.
type ProcessInitializationOutcome struct {
	admission   ProcessAdmission
	incarnation TreeIncarnationID
	failure     Failure
}

func (p ProcessInitializationOutcome) Admission() ProcessAdmission { return p.admission }

// Incarnation identifies the physical initialization attempt within its tree. A
// child re-attempted after recovery carries a new incarnation for the same
// admission, so the Host distinguishes a retry from the attempt it superseded. A
// root is admitted exactly once and carries the zero incarnation.
func (p ProcessInitializationOutcome) Incarnation() TreeIncarnationID { return p.incarnation }

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
// later persistence. A child whose attempt is interrupted before its result
// persists is re-attempted after recovery under a new incarnation, so one
// admission can present more than one outcome; each carries the incarnation of
// its attempt and the published tree identifies the live one. Initialization
// waits for this call, so implementations must be bounded, concurrency-safe,
// idempotent per (admission, incarnation), and must not re-enter the Engine. ctx
// keeps Host values without cancellation so an accepted admission can finish
// while its parent terminates; the Host applies its own deadline and reconciles
// an uncertain acknowledgment by that identity. Restore produces no outcome.
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

func initializedProcessOutcome(admission ProcessAdmission, incarnation TreeIncarnationID) ProcessInitializationOutcome {
	return ProcessInitializationOutcome{admission: admission, incarnation: incarnation}
}

func failedProcessInitializationOutcome(admission ProcessAdmission, incarnation TreeIncarnationID, failure Failure) ProcessInitializationOutcome {
	return ProcessInitializationOutcome{admission: admission, incarnation: incarnation, failure: failure}
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
