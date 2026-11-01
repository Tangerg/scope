package agent

import (
	"github.com/Tangerg/scope/agent/run"
)

type runtimeError struct {
	failure run.FailureClassification
}

func (r runtimeError) Error() string {
	return "agent runtime stopped: " + r.failure.Kind().String() + "/" + r.failure.Code()
}

func (r runtimeError) ErrorType() string { return r.failure.Code() }
