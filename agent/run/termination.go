package run

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"

	"github.com/Tangerg/scope/agent/internal/jsonwire"
)

const maxTerminationReasonBytes = 4096

var errInvalidTermination = errors.New("run: invalid termination")

type TerminationCause string

const (
	TerminationCauseInvalid            TerminationCause = ""
	TerminationCauseCompletion         TerminationCause = "completion"
	TerminationCauseRuntimeKill        TerminationCause = "runtime_kill"
	TerminationCauseParentDeadline     TerminationCause = "parent_deadline"
	TerminationCauseHostDeadline       TerminationCause = "host_deadline"
	TerminationCauseParentCancellation TerminationCause = "parent_cancellation"
	TerminationCauseHostCancellation   TerminationCause = "host_cancellation"
	TerminationCauseExecutionFailure   TerminationCause = "execution_failure"
	TerminationCauseContractFailure    TerminationCause = "contract_failure"
	TerminationCauseExternalFailure    TerminationCause = "external_failure"
	TerminationCausePanic              TerminationCause = "panic"
)

func (t TerminationCause) Valid() bool {
	switch t {
	case TerminationCauseCompletion, TerminationCauseRuntimeKill,
		TerminationCauseParentDeadline,
		TerminationCauseHostDeadline, TerminationCauseParentCancellation,
		TerminationCauseHostCancellation, TerminationCauseExecutionFailure,
		TerminationCauseContractFailure, TerminationCauseExternalFailure,
		TerminationCausePanic:
		return true
	default:
		return false
	}
}

func (t TerminationCause) String() string {
	if !t.Valid() {
		return invalidEnumName
	}
	return string(t)
}

func (t TerminationCause) status() Status {
	switch t {
	case TerminationCauseCompletion:
		return StatusCompleted
	case TerminationCauseRuntimeKill:
		return StatusKilled
	case TerminationCauseParentDeadline, TerminationCauseHostDeadline:
		return StatusTimedOut
	case TerminationCauseParentCancellation, TerminationCauseHostCancellation:
		return StatusCanceled
	case TerminationCauseExecutionFailure, TerminationCauseContractFailure,
		TerminationCauseExternalFailure, TerminationCausePanic:
		return StatusFailed
	default:
		return StatusInvalid
	}
}

// Termination is the immutable result of applying the terminal priority matrix.
// Only the Runtime creates terminal facts from validated control intents and
// Step outcomes; callers obtain this observation from Result or decode it.
// Failure owns failed causes and diagnostics, Output owns completion, and
// the remaining control causes own their reason. Status is always derived
// from the cause.
type Termination struct {
	cause   TerminationCause
	reason  string
	failure Failure
	output  Payload
}

func validateTerminationReason(reason string) error {
	if !validBoundedText(reason, maxTerminationReasonBytes) {
		return fmt.Errorf("%w: reason must be non-empty, trimmed UTF-8 within %d bytes", errInvalidTermination, maxTerminationReasonBytes)
	}
	return nil
}

func (t Termination) Status() Status { return t.Cause().status() }

func (t Termination) Cause() TerminationCause {
	switch {
	case t.failure.Valid():
		return t.failure.Kind().terminationCause()
	case t.output.Valid():
		return TerminationCauseCompletion
	default:
		return t.cause
	}
}

// Reason returns a bounded diagnostic reason. Completion has an empty reason.
func (t Termination) Reason() string {
	if t.failure.Valid() {
		return t.failure.Message()
	}
	return t.reason
}

// Output returns the final semantic result only for StatusCompleted. A
// present JSON null is a completed output.
func (t Termination) Output() (Payload, bool) { return t.output, t.output.Valid() }

func (t Termination) Failure() (Failure, bool) {
	return t.failure, t.Status() == StatusFailed
}

func (t Termination) Valid() bool {
	if !t.Status().Terminal() {
		return false
	}
	switch {
	case t.failure.Valid():
		return t.cause == TerminationCauseInvalid && t.reason == "" && !t.output.Valid()
	case t.failure != (Failure{}):
		return false
	case t.output.Valid():
		return t.cause == TerminationCauseInvalid && t.reason == ""
	default:
		// Completion and failure causes are owned by Output and Failure.
		return t.cause != TerminationCauseCompletion && t.Status() != StatusFailed &&
			validateTerminationReason(t.reason) == nil
	}
}

func (t Termination) MarshalJSON() ([]byte, error) {
	if !t.Valid() {
		return nil, errInvalidTermination
	}
	wire := terminationWire{
		Cause:  t.cause,
		Reason: t.reason,
		Output: t.output,
	}
	if t.failure.Valid() {
		wire.Failure = &t.failure
	}
	return jsonv2.Marshal(wire)
}

func (t *Termination) UnmarshalJSON(data []byte) error {
	if t == nil {
		return fmt.Errorf("%w: nil receiver", errInvalidTermination)
	}
	wire, err := jsonwire.Decode[terminationWire](data)
	if err != nil {
		return fmt.Errorf("%w: decode: %w", errInvalidTermination, err)
	}
	value := Termination{
		cause: wire.Cause, reason: wire.Reason, output: wire.Output,
	}
	if wire.Failure != nil {
		value.failure = *wire.Failure
	}
	if !value.Valid() {
		return errInvalidTermination
	}
	*t = value
	return nil
}

type terminationWire struct {
	Cause   TerminationCause `json:"cause,omitzero"`
	Reason  string           `json:"reason,omitempty"`
	Failure *Failure         `json:"failure,omitzero"`
	Output  Payload          `json:"output,omitzero"`
}

func (Termination) JSONSchemaModel() any { return terminationWire{} }

func (t Termination) equal(other Termination) bool {
	return t.Cause() == other.Cause() && t.reason == other.reason && t.failure == other.failure && t.output.Equal(other.output)
}

func (t Termination) accepts(cause TerminationCause) bool {
	return !t.Valid() || cause.priority() > t.Cause().priority()
}

type terminationPriority uint8

const (
	terminationPriorityOutcome terminationPriority = iota
	terminationPriorityCancellation
	terminationPriorityDeadline
	terminationPriorityKill
)

func (t TerminationCause) priority() terminationPriority {
	switch t {
	case TerminationCauseRuntimeKill:
		return terminationPriorityKill
	case TerminationCauseHostDeadline, TerminationCauseParentDeadline:
		return terminationPriorityDeadline
	case TerminationCauseHostCancellation, TerminationCauseParentCancellation:
		return terminationPriorityCancellation
	default:
		return terminationPriorityOutcome
	}
}
