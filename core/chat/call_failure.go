package chat

import (
	"errors"
	"fmt"

	"github.com/samber/lo"
)

var ErrInvalidCallFailure = errors.New("chat: invalid call failure")

type CallFailureKind string

// Failed establishes an unsuccessful invocation, not absence of side effects
// or token use. Only NotExecuted proves no provider work ran in this invocation.
const (
	CallFailureInvalid     CallFailureKind = ""
	CallFailureUnknown     CallFailureKind = "unknown"
	CallFailureFailed      CallFailureKind = "failed"
	CallFailureNotExecuted CallFailureKind = "not_executed"
)

func (c CallFailureKind) Valid() bool {
	return c == CallFailureUnknown || c == CallFailureFailed || c == CallFailureNotExecuted
}

// CallFailure owns evidence for the complete current Call or Stream invocation.
// Cause remains inspectable through errors.Is and errors.As, but failures inside
// it belong to nested invocations and cannot override Kind. A composition that
// already executed work must establish its own boundary, including Unknown when
// it cannot establish the complete outcome. No kind authorizes a retry.
type CallFailure struct {
	Kind  CallFailureKind
	Cause error
}

func (c *CallFailure) Error() string {
	if c == nil || !c.Kind.Valid() || lo.IsNil(c.Cause) {
		return ErrInvalidCallFailure.Error()
	}
	return fmt.Sprintf("chat: %s: %v", c.Kind, c.Cause)
}

func (c *CallFailure) Unwrap() error {
	if c == nil || lo.IsNil(c.Cause) {
		return nil
	}
	return c.Cause
}

func (c *CallFailure) Validate() error {
	if c == nil || !c.Kind.Valid() || lo.IsNil(c.Cause) {
		return ErrInvalidCallFailure
	}
	return nil
}

// ClassifyFailure is the execution-evidence boundary; errors.Is and errors.As
// inspect diagnostics, not the outcome of a composite invocation. Transparent
// wrapping preserves one boundary. Joined independent boundaries require an
// enclosing owner to classify the whole call, otherwise its outcome is unknown.
// Nil has no failure; an ordinary or invalid error establishes only Unknown.
func ClassifyFailure(err error) CallFailureKind {
	if err == nil {
		return CallFailureInvalid
	}
	return classifyCallFailure(err)
}

//nolint:errorlint // Each node owns one scope; errors.As would skip invocation and join boundaries.
func classifyCallFailure(err error) CallFailureKind {
	if lo.IsNil(err) {
		return CallFailureUnknown
	}
	if failure, scoped := err.(*CallFailure); scoped {
		if failure.Validate() != nil {
			return CallFailureUnknown
		}
		return failure.Kind
	}
	switch wrapped := err.(type) {
	case interface{ Unwrap() error }:
		return classifyCallFailure(wrapped.Unwrap())
	case interface{ Unwrap() []error }:
		var cause error
		for _, candidate := range wrapped.Unwrap() {
			if candidate == nil {
				continue
			}
			if cause != nil {
				return CallFailureUnknown
			}
			cause = candidate
		}
		return classifyCallFailure(cause)
	default:
		return CallFailureUnknown
	}
}
