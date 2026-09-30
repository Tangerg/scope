// Package stepfail builds the classified failures built-in strategies end
// their Steps with, so every strategy normalizes diagnostic text the same way.
package stepfail

import (
	"github.com/Tangerg/scope/agent"
)

// Failure classifies a strategy failure. The message is normalized, so
// dynamic text such as an error's message always forms a valid Failure.
func Failure(kind agent.FailureKind, code, message string) (agent.Failure, error) {
	return agent.NewFailure(kind, code, agent.NormalizeDiagnostic(message))
}

// Transition ends a Step with the Failure that kind, code, and message classify.
func Transition(consumedSignals uint32, kind agent.FailureKind, code, message string) (agent.Transition, error) {
	failure, err := Failure(kind, code, message)
	if err != nil {
		return agent.Transition{}, err
	}
	return agent.Fail(consumedSignals, failure)
}
