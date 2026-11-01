// Package stepfail builds the classified failures built-in strategies end
// their Steps with, so every strategy normalizes diagnostic text the same way.
package stepfail

import (
	"github.com/Tangerg/scope/agent/run"
)

// Failure classifies a strategy failure. The message is normalized, so
// dynamic text such as an error's message always forms a valid Failure.
func Failure(kind run.FailureKind, code, message string) (run.Failure, error) {
	return run.NewFailure(kind, code, run.NormalizeDiagnostic(message))
}

func Transition(consumedSignals uint32, kind run.FailureKind, code, message string) (run.Transition, error) {
	failure, err := Failure(kind, code, message)
	if err != nil {
		return run.Transition{}, err
	}
	return run.Fail(consumedSignals, failure)
}
