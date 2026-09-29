package agent

import (
	"context"
	"errors"
)

// StepError discards the entire candidate Step, including input consumption and
// Effects, while preserving Failure in the Process result. Cause optionally
// retains in-memory evidence for errors.Is/As; only Failure is persisted.
// A valid Failure is required. Cancellation and panic containment remain owned
// by the Engine and take precedence over Strategy classification.
type StepError struct {
	Failure Failure
	Cause   error
}

func (s *StepError) Error() string { return s.Failure.Message() }

func (s *StepError) Unwrap() error { return s.Cause }

// ClassifiedError is a sentinel that owns the Failure kind and code persisted
// when an error wrapping it leaves Execution.Step, so no separate mapping can
// disagree with the declaration.
type ClassifiedError struct {
	failure Failure
}

// NewClassifiedError declares one sentinel together with the Failure the Engine
// persists for it; ClassifyStepError replaces message with the complete wrapped
// diagnostic. An invalid kind, code, or message is a programming error and
// panics.
func NewClassifiedError(kind FailureKind, code, message string) *ClassifiedError {
	failure, err := NewFailure(kind, code, message)
	if err != nil {
		panic(err)
	}
	return &ClassifiedError{failure: failure}
}

func (c *ClassifiedError) Error() string { return c.failure.Message() }

// ClassifyStepError returns the error Execution.Step must return for err. An
// error wrapping a ClassifiedError becomes a *StepError carrying that
// classification and the complete diagnostic; every other error is returned
// unchanged and recorded as execution.step.failed. Cancellation, contained
// panics, and an existing *StepError outrank Strategy classification.
func ClassifyStepError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	// A contained callback panic keeps FailureKindPanic. Classifying it would
	// report a deliberate contract decision where a callback crashed.
	if _, contained := errors.AsType[*CallbackPanicError](err); contained {
		return err
	}
	if _, sealed := errors.AsType[*StepError](err); sealed {
		return err
	}
	classified, ok := errors.AsType[*ClassifiedError](err)
	if !ok {
		return err
	}
	return &StepError{
		Failure: newEngineFailure(classified.failure.Kind(), classified.failure.Code(), err),
		Cause:   err,
	}
}
