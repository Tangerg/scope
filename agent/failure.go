package agent

import (
	"encoding/json"
	"errors"
	"fmt"
)

const maxFailureCodeBytes = 128

var ErrInvalidFailure = errors.New("agent: invalid failure")

// FailureKind stays independent of retryability so classifying a failure
// cannot silently select a recovery or business policy.
type FailureKind string

const (
	FailureKindInvalid   FailureKind = ""
	FailureKindExecution FailureKind = "execution"
	FailureKindContract  FailureKind = "contract"
	FailureKindExternal  FailureKind = "external"
	FailureKindPanic     FailureKind = "panic"
)

func (f FailureKind) Valid() bool {
	switch f {
	case FailureKindExecution, FailureKindContract, FailureKindExternal, FailureKindPanic:
		return true
	default:
		return false
	}
}

func (f FailureKind) String() string {
	if !f.Valid() {
		return invalidEnumName
	}
	return string(f)
}

// Failure separates stable codes from diagnostic text so wording changes cannot
// alter control flow. Its bounded UTF-8 message must survive snapshot JSON and
// must exclude secrets.
type Failure struct {
	kind    FailureKind
	code    string
	message string
}

// NewFailure requires a kind and code alongside the message so callers
// classify failures programmatically. Matching on message text is what makes
// error handling break on wording changes, and it does not survive the
// snapshot round trip. Message must be trimmed, valid UTF-8 and contain at
// most 4096 bytes.
func NewFailure(kind FailureKind, code, message string) (Failure, error) {
	if !kind.Valid() {
		return Failure{}, fmt.Errorf("%w: kind is required", ErrInvalidFailure)
	}
	if !ValidQualifiedName(code) || len(code) > maxFailureCodeBytes {
		return Failure{}, fmt.Errorf("%w: code must be a lowercase qualified name containing at most %d bytes", ErrInvalidFailure, maxFailureCodeBytes)
	}
	if !ValidDiagnostic(message) {
		return Failure{}, fmt.Errorf("%w: message must be non-empty, trimmed UTF-8 within %d bytes", ErrInvalidFailure, MaxDiagnosticBytes)
	}
	return Failure{kind: kind, code: code, message: message}, nil
}

func (f Failure) Kind() FailureKind { return f.kind }

func (f Failure) Code() string { return f.code }

func (f Failure) Message() string { return f.message }

func (f Failure) Valid() bool {
	return f.kind.Valid()
}

// Kernel classifications are fixed by their owning boundary. An invalid kind or
// code is a programming error; substituting another Failure would hide its cause.
func newEngineFailure(kind FailureKind, code string, err error) Failure {
	message := ""
	if err != nil {
		message = err.Error()
	}
	message = NormalizeDiagnostic(message)
	failure, failureErr := NewFailure(kind, code, message)
	if failureErr != nil {
		panic(failureErr)
	}
	return failure
}

func (f Failure) MarshalJSON() ([]byte, error) {
	if !f.Valid() {
		return nil, ErrInvalidFailure
	}
	return json.Marshal(failureWire{Kind: f.kind, Code: f.code, Message: f.message})
}

func (f *Failure) UnmarshalJSON(data []byte) error {
	if f == nil {
		return fmt.Errorf("%w: nil receiver", ErrInvalidFailure)
	}
	wire, err := decodeJSON[failureWire](data)
	if err != nil {
		return fmt.Errorf("%w: decode: %w", ErrInvalidFailure, err)
	}
	value, err := NewFailure(wire.Kind, wire.Code, wire.Message)
	if err != nil {
		return err
	}
	*f = value
	return nil
}

type failureWire struct {
	Kind    FailureKind `json:"kind"`
	Code    string      `json:"code"`
	Message string      `json:"message"`
}

func (Failure) JSONSchemaAlias() any { return failureWire{} }

func (f Failure) terminationCause() TerminationCause {
	cause := TerminationCauseExecutionFailure
	switch f.Kind() {
	case FailureKindContract:
		cause = TerminationCauseContractFailure
	case FailureKindExternal:
		cause = TerminationCauseExternalFailure
	case FailureKindPanic:
		cause = TerminationCausePanic
	}
	return cause
}

func (f Failure) termination() Termination {
	return Termination{status: StatusFailed, cause: f.terminationCause(), reason: f.Message(), failure: f}
}

const (
	failureCodeEngineCapabilityDenied                         = "engine.capability.denied"
	failureCodeEngineChildAdmissionRejected                   = "engine.child.admission.rejected"
	failureCodeEngineChildBudgetExhausted                     = "engine.child.budget_exhausted"
	failureCodeEngineChildBudgetInvalid                       = "engine.child.budget_invalid"
	failureCodeEngineChildCapabilityEscalation                = "engine.child.capability_escalation"
	failureCodeEngineChildControlInvalid                      = "engine.child.control.invalid"
	failureCodeEngineChildControlNotOwned                     = "engine.child.control.not_owned"
	failureCodeEngineChildControlSettlementInvalid            = "engine.child.control.settlement.invalid"
	failureCodeEngineChildDeploymentUnavailable               = "engine.child.deployment_unavailable"
	failureCodeEngineChildIdentityConflict                    = "engine.child.identity_conflict"
	failureCodeEngineChildInitializationOutcomeUnacknowledged = "engine.child.initialization_outcome.unacknowledged"
	failureCodeEngineChildInputInvalid                        = "engine.child.input.invalid"
	failureCodeEngineChildRequestInvalid                      = "engine.child.request.invalid"
	failureCodeEngineChildSettlementInvalid                   = "engine.child.settlement.invalid"
	failureCodeEngineChildSignalRejected                      = "engine.child.signal.rejected"
	failureCodeEngineChildStartInterrupted                    = "engine.child.start.interrupted"
	failureCodeEngineChildStartUnavailable                    = "engine.child.start.unavailable"
	failureCodeEngineChildTreeLimit                           = "engine.child.tree_limit"
	failureCodeEngineChildWaitSatisfactionEncodingFailed      = "engine.child.wait.satisfaction.encoding_failed"
	failureCodeEngineChildWaitSatisfactionInvalid             = "engine.child.wait.satisfaction.invalid"
	failureCodeEngineCommittedExecutionStateInvalid           = "engine.committed_execution_state.invalid"
	failureCodeEngineDispatchCanceled                         = "engine.dispatch.canceled"
	failureCodeEngineDispatchDeadline                         = "engine.dispatch.deadline"
	failureCodeEngineDispatchFailed                           = "engine.dispatch.failed"
	failureCodeEngineDispatchPanicked                         = "engine.dispatch.panicked"
	failureCodeEngineDispatchSettlementInvalid                = "engine.dispatch.settlement.invalid"
	failureCodeEngineEffectPhaseInvalid                       = "engine.effect.phase.invalid"
	failureCodeEngineEffectRecoveryInvalid                    = "engine.effect.recovery.invalid"
	failureCodeEngineEffectSettlementInvalid                  = "engine.effect.settlement.invalid"
	failureCodeEngineFinalizeInvalid                          = "engine.finalize.invalid"
	failureCodeEngineFrameworkEffectSettlementInvalid         = "engine.framework_effect.settlement.invalid"
	failureCodeEngineLimitChildWaitSignal                     = "engine.limit.child_wait_signal"
	failureCodeEngineLimitEffects                             = "engine.limit.effects"
	failureCodeEngineLimitSignals                             = "engine.limit.signals"
	failureCodeEngineLimitSnapshot                            = "engine.limit.snapshot"
	failureCodeEngineLimitSteps                               = "engine.limit.steps"
	failureCodeEngineProcessAttemptExhausted                  = "engine.process.attempt_exhausted"
	failureCodeEngineProcessSnapshotFailed                    = "engine.process.snapshot.failed"
	failureCodeEngineProcessSnapshotUnrestorable              = "engine.process.snapshot.unrestorable"
	failureCodeEngineProcessStartFailed                       = "engine.process.start.failed"
	failureCodeEngineTerminationInvalid                       = "engine.termination.invalid"
	failureCodeEngineTreeDurabilityConflict                   = "engine.tree.durability_conflict"
	failureCodeEngineTreeDurabilityFailed                     = "engine.tree.durability_failed"
	failureCodeEngineTreeIncarnationConflict                  = "engine.tree.incarnation_conflict"
	failureCodeExecutionEffectInvalid                         = "execution.effect.invalid"
	failureCodeExecutionOutputInvalid                         = "execution.output.invalid"
	failureCodeExecutionSnapshotFailed                        = "execution.snapshot.failed"
	failureCodeExecutionSnapshotUnrestorable                  = "execution.snapshot.unrestorable"
	failureCodeExecutionStepFailed                            = "execution.step.failed"
	failureCodeExecutionTransitionInvalid                     = "execution.transition.invalid"
)
