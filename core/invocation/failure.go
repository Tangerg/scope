package invocation

import (
	"errors"
	"fmt"

	"github.com/samber/lo"
)

var ErrInvalidFailure = errors.New("invocation: invalid call failure")

type FailureKind string

// None classifies a nil error and is never the Kind of a Failure. Failed
// establishes an unsuccessful invocation, not absence of side effects or token
// use. Only NotExecuted proves no provider work ran in this invocation.
const (
	FailureNone        FailureKind = ""
	FailureUnknown     FailureKind = "unknown"
	FailureFailed      FailureKind = "failed"
	FailureNotExecuted FailureKind = "not_executed"
)

func (f FailureKind) Valid() bool {
	return f == FailureUnknown || f == FailureFailed || f == FailureNotExecuted
}

// Failure owns evidence for the complete current Call or Stream invocation.
// Cause remains inspectable through errors.Is and errors.As, but failures inside
// it belong to nested invocations and cannot override Kind. A composition that
// already executed work must establish its own boundary, including Unknown when
// it cannot establish the complete outcome. No kind authorizes a retry.
type Failure struct {
	Kind  FailureKind
	Cause error
}

func (f *Failure) Error() string {
	if f == nil || !f.Kind.Valid() || lo.IsNil(f.Cause) {
		return ErrInvalidFailure.Error()
	}
	return fmt.Sprintf("invocation: %s: %v", f.Kind, f.Cause)
}

func (f *Failure) Unwrap() error {
	if f == nil || lo.IsNil(f.Cause) {
		return nil
	}
	return f.Cause
}

func (f *Failure) Validate() error {
	if f == nil || !f.Kind.Valid() || lo.IsNil(f.Cause) {
		return ErrInvalidFailure
	}
	return nil
}

// ClassifyFailure is the execution-evidence boundary; errors.Is and errors.As
// inspect diagnostics, not the outcome of a composite invocation. Transparent
// wrapping preserves one boundary. Joined independent boundaries require an
// enclosing owner to classify the whole call, otherwise its outcome is unknown.
// Nil has no failure; an ordinary or invalid error establishes only Unknown.
func ClassifyFailure(err error) FailureKind {
	if err == nil {
		return FailureNone
	}
	return classifyFailure(err)
}

//nolint:errorlint // Each node owns one scope; errors.As would skip invocation and join boundaries.
func classifyFailure(err error) FailureKind {
	if lo.IsNil(err) {
		return FailureUnknown
	}
	if failure, scoped := err.(*Failure); scoped {
		if failure.Validate() != nil {
			return FailureUnknown
		}
		return failure.Kind
	}
	switch wrapped := err.(type) {
	case interface{ Unwrap() error }:
		return classifyFailure(wrapped.Unwrap())
	case interface{ Unwrap() []error }:
		var cause error
		for _, candidate := range wrapped.Unwrap() {
			if candidate == nil {
				continue
			}
			if cause != nil {
				return FailureUnknown
			}
			cause = candidate
		}
		return classifyFailure(cause)
	default:
		return FailureUnknown
	}
}
