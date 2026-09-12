package agent

import (
	agent "github.com/Tangerg/scope/agent"
)

type runtimeFactError struct {
	kind agent.FailureKind
	code string
}

func (r runtimeFactError) Error() string {
	return "agent runtime stopped: " + r.kind.String() + "/" + r.code
}

func (r runtimeFactError) ErrorType() string { return r.code }
