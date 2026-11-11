// Package stepfail builds the classified failures built-in strategies record:
// the Transitions that end their Steps and the failed settlements their
// dispatchers return.
package stepfail

import (
	"github.com/Tangerg/scope/agent/run"
)

func Transition(consumedMessages uint32, kind run.FailureKind, code, message string) (run.Transition, error) {
	failure, err := run.NewFailure(kind, code, message)
	if err != nil {
		return run.Transition{}, err
	}
	return run.Fail(consumedMessages, failure)
}

// Settlement is a definite dispatcher failure that carries no domain details.
func Settlement(kind run.FailureKind, code, message string) (run.Settlement, error) {
	failure, err := run.NewFailure(kind, code, message)
	if err != nil {
		return run.Settlement{}, err
	}
	return run.NewFailedSettlement(failure, run.Payload{})
}
