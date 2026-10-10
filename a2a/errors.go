package a2a

import (
	"errors"
	"fmt"

	sdka2a "github.com/a2aproject/a2a-go/v2/a2a"
)

var (
	ErrNilCard     = errors.New("a2a: agent card must not be nil")
	ErrInvalidCard = errors.New("a2a: invalid agent card")

	ErrNilTaskStore = errors.New("a2a: task store must not be nil")

	ErrNilAgent = errors.New("a2a: agent must not be nil")

	ErrEmptyCardURL       = errors.New("a2a: card URL must not be empty")
	ErrInvalidCardURL     = errors.New("a2a: invalid card URL")
	ErrInvalidCardTimeout = errors.New("a2a: card timeout must not be negative")
	ErrInvalidRPCOrigin   = errors.New("a2a: invalid allowed RPC origin")
	ErrOriginNotAllowed   = errors.New("a2a: origin not allowed")

	ErrInvalidRPCInterface = errors.New("a2a: invalid RPC interface")

	ErrInvalidResult = errors.New("a2a: invalid send-message result")
)

var errNilAgentSequence = errors.New("a2a: agent returned a nil output sequence")

// RemoteAgentError reports that a reached remote A2A task did not complete, as
// distinct from transport or protocol failures. A task that failed, was
// canceled, or was rejected carries it as the Cause of a definite
// core/tool.Failure; a task that requires unsupported continuation returns it
// as an ordinary error, because the remote task may still advance.
type RemoteAgentError struct {
	State sdka2a.TaskState

	Detail string
}

func (r *RemoteAgentError) Error() string {
	if r == nil {
		return "a2a: remote agent task did not complete successfully"
	}
	if r.Detail != "" {
		return fmt.Sprintf("a2a: remote agent task did not complete successfully (state %s): %s", r.State, r.Detail)
	}
	return fmt.Sprintf("a2a: remote agent task did not complete successfully (state %s)", r.State)
}
