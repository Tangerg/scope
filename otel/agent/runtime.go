package agent

import (
	agent "github.com/Tangerg/scope/agent"
)

type runtimeError struct {
	failure agent.FailureClassification
}

func (r runtimeError) Error() string {
	return "agent runtime stopped: " + r.failure.Kind().String() + "/" + r.failure.Code()
}

func (r runtimeError) ErrorType() string { return r.failure.Code() }
