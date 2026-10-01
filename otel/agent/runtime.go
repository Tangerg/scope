package agent

import (
	agent "github.com/Tangerg/scope/agent"
)

type runtimeFactError struct {
	failure agent.FailureClassification
}

func (r runtimeFactError) Error() string {
	return "agent runtime stopped: " + r.failure.Kind().String() + "/" + r.failure.Code()
}

func (r runtimeFactError) ErrorType() string { return r.failure.Code() }
