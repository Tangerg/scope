// Package stepfail builds the classified failures built-in strategies record:
// the Failures that end their Steps and the failed settlements their
// dispatchers return, so every strategy normalizes diagnostic text the same way.
package stepfail

import (
	"github.com/Tangerg/scope/agent/run"
)

// Failure classifies a strategy failure. The message is normalized, so
// dynamic text such as an error's message always forms a valid Failure.
func Failure(kind run.FailureKind, code, message string) (run.Failure, error) {
	return run.NewFailure(kind, code, run.NormalizeDiagnostic(message))
}

func Transition(consumedMessages uint32, kind run.FailureKind, code, message string) (run.Transition, error) {
	failure, err := Failure(kind, code, message)
	if err != nil {
		return run.Transition{}, err
	}
	return run.Fail(consumedMessages, failure)
}

// Settlement is a definite dispatcher failure that carries no domain details.
func Settlement(kind run.FailureKind, code, message string) (run.Settlement, error) {
	failure, err := Failure(kind, code, message)
	if err != nil {
		return run.Settlement{}, err
	}
	return run.NewFailedSettlement(failure, run.Payload{})
}
