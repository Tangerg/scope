package run

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"

	"github.com/Tangerg/scope/agent/internal/enum"
	"github.com/Tangerg/scope/agent/internal/jsonwire"
)

var ErrInvalidFailure = errors.New("run: invalid failure")

// FailureKind stays independent of retryability so classifying a failure
// cannot silently select a recovery or business policy.
type FailureKind string

const (
	FailureKindInvalid FailureKind = ""
	// FailureKindExecution reports a Strategy error or an exhausted execution bound.
	FailureKindExecution FailureKind = "execution"
	// FailureKindContract reports an invalid protocol value or authority violation.
	FailureKindContract FailureKind = "contract"
	// FailureKindExternal reports a Host boundary error without a contained panic.
	FailureKindExternal FailureKind = "external"
	// FailureKindPanic reports a contained callback panic when that boundary produces
	// a Failure. Storage and API errors retain CallbackPanicError instead. This kind
	// does not establish an external operation's outcome or authorize replay.
	FailureKindPanic FailureKind = "panic"
)

func (f FailureKind) Valid() bool {
	switch f {
	case FailureKindExecution, FailureKindContract, FailureKindExternal, FailureKindPanic:
		return true
	default:
		return false
	}
}

func (f FailureKind) String() string { return enum.String(f) }

func (f FailureKind) terminationCause() TerminationCause {
	switch f {
	case FailureKindContract:
		return TerminationCauseContractFailure
	case FailureKindExternal:
		return TerminationCauseExternalFailure
	case FailureKindPanic:
		return TerminationCausePanic
	default:
		return TerminationCauseExecutionFailure
	}
}

// FailureClassification is the stable, diagnostic-free part of a Failure.
// Observations carry only this part, so they can be recorded and aggregated
// without exposing diagnostic text.
type FailureClassification struct {
	kind FailureKind
	code string
}

func (f FailureClassification) Kind() FailureKind { return f.kind }

func (f FailureClassification) Code() string { return f.code }

func (f FailureClassification) Valid() bool {
	return f.kind.Valid() && ValidQualifiedName(f.code)
}

// Failure separates stable codes from diagnostic text so wording changes cannot
// alter control flow. Its bounded UTF-8 message must survive snapshot JSON and
// must exclude secrets.
type Failure struct {
	kind    FailureKind
	code    string
	message string
}

// NewFailure records message as NormalizeDiagnostic renders it, so any error's
// text forms a valid Failure. Callers remain responsible for excluding secrets.
func NewFailure(kind FailureKind, code, message string) (Failure, error) {
	return newFailure(kind, code, NormalizeDiagnostic(message))
}

// newFailure is the strict form decoding uses: persisted text must already be
// normalized, because normalizing it would change what the log recorded.
func newFailure(kind FailureKind, code, message string) (Failure, error) {
	if !(FailureClassification{kind: kind, code: code}).Valid() {
		return Failure{}, fmt.Errorf("%w: kind is required and code must be a lowercase qualified name containing at most %d bytes", ErrInvalidFailure, maxQualifiedNameBytes)
	}
	if !ValidDiagnostic(message) {
		return Failure{}, fmt.Errorf("%w: message must be non-empty, trimmed UTF-8 within %d bytes", ErrInvalidFailure, MaxDiagnosticBytes)
	}
	return Failure{kind: kind, code: code, message: message}, nil
}

func (f Failure) Kind() FailureKind { return f.kind }

func (f Failure) Code() string { return f.code }

func (f Failure) Message() string { return f.message }

func (f Failure) Classification() FailureClassification {
	return FailureClassification{kind: f.kind, code: f.code}
}

func (f Failure) Valid() bool {
	return f.kind.Valid()
}

func failureKindForError(err error, fallback FailureKind) FailureKind {
	if sealed, ok := errors.AsType[*callbackError](err); ok && sealed != nil && sealed.panicked {
		return FailureKindPanic
	}
	return fallback
}

// Kernel classifications are fixed by their owning boundary. An invalid kind or
// code is a programming error; substituting another Failure would hide its cause.
func newRuntimeFailure(kind FailureKind, code string, err error) Failure {
	message := ""
	if err != nil {
		message = err.Error()
	}
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
	return jsonv2.Marshal(failureWire{Kind: f.kind, Code: f.code, Message: f.message})
}

func (f *Failure) UnmarshalJSON(data []byte) error {
	if f == nil {
		return fmt.Errorf("%w: nil receiver", ErrInvalidFailure)
	}
	wire, err := jsonwire.Decode[failureWire](data)
	if err != nil {
		return fmt.Errorf("%w: decode: %w", ErrInvalidFailure, err)
	}
	value, err := newFailure(wire.Kind, wire.Code, wire.Message)
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

func (Failure) JSONSchemaModel() any { return failureWire{} }

func (f Failure) termination() Termination {
	return Termination{failure: f}
}

const (
	failureCodeRuntimeResourceLimitExceeded       = "runtime.resource.limit_exceeded"
	failureCodeRuntimeCapabilityDenied            = "runtime.capability.denied"
	failureCodeRuntimeChildAdmissionRejected      = "runtime.child.admission.rejected"
	failureCodeRuntimeChildRequestCanceled        = "runtime.child.request.canceled"
	failureCodeRuntimeChildRequestInvalid         = "runtime.child.request.invalid"
	failureCodeRuntimeCounterExhausted            = "runtime.counter.exhausted"
	failureCodeRuntimeDispatchFailed              = "runtime.dispatch.failed"
	failureCodeRuntimeDispatchPanicked            = "runtime.dispatch.panicked"
	failureCodeRuntimeDispatchSettlementInvalid   = "runtime.dispatch.settlement.invalid"
	failureCodeRuntimeEffectRecoveryInvalid       = "runtime.effect.recovery.invalid"
	failureCodeRuntimeProcessSnapshotFailed       = "runtime.process.snapshot.failed"
	failureCodeRuntimeProcessSnapshotUnrestorable = "runtime.process.snapshot.unrestorable"
	failureCodeRuntimeProcessStartFailed          = "runtime.process.start.failed"
	failureCodeExecutionEffectInvalid             = "execution.effect.invalid"
	failureCodeExecutionStepFailed                = "execution.step.failed"
	failureCodeExecutionTransitionInvalid         = "execution.transition.invalid"
)
