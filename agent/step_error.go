package agent

import (
	"context"
	"errors"
)

// ClassifiedError is a sentinel that owns the Failure kind and code persisted
// when an error wrapping it leaves Execution.Step, so no separate mapping can
// disagree with the declaration.
type ClassifiedError struct {
	failure Failure
}

// NewClassifiedError declares one sentinel together with the Failure the Engine
// persists when Execution.Step returns an error wrapping it; the persisted
// message is the complete wrapped diagnostic. An invalid kind, code, or
// message is a programming error and panics.
func NewClassifiedError(kind FailureKind, code, message string) *ClassifiedError {
	failure, err := NewFailure(kind, code, message)
	if err != nil {
		panic(err)
	}
	return &ClassifiedError{failure: failure}
}

func (c *ClassifiedError) Error() string { return c.failure.Message() }

// classifiedStepFailure is the Failure the Engine persists when Execution.Step
// returns err wrapping a ClassifiedError, with the complete wrapped chain as
// its diagnostic. Cancellation and contained panics outrank Strategy
// classification, and any other error stays unclassified.
func classifiedStepFailure(err error) (Failure, bool) {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return Failure{}, false
	}
	if _, contained := errors.AsType[*CallbackPanicError](err); contained {
		return Failure{}, false
	}
	classified, ok := errors.AsType[*ClassifiedError](err)
	if !ok {
		return Failure{}, false
	}
	return newEngineFailure(classified.failure.Kind(), classified.failure.Code(), err), true
}

// StepFailure reports the Failure the Engine persists when Execution.Step
// returns err. It reports false for an error the Engine records as
// execution.step.failed, and for cancellation and contained panics, whose
// classifications the Engine owns.
func StepFailure(err error) (Failure, bool) { return classifiedStepFailure(err) }
