package a2a

import (
	"errors"
	"fmt"

	sdka2a "github.com/a2aproject/a2a-go/v2/a2a"
)

var (
	ErrNilCard     = errors.New("a2a: agent card must not be nil")
	ErrInvalidCard = errors.New("a2a: invalid agent card")

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

// RemoteAgentError reports that a remote A2A task did not complete
// successfully. It lets a caller use [errors.AsType] to distinguish it from
// transport or protocol failures: the remote was reached and answered, but the
// work failed, was canceled or rejected, or requires unsupported continuation.
type RemoteAgentError struct {
	// State is the task state the remote reported.
	State sdka2a.TaskState
	// Detail is any human-readable message the remote attached, or "".
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
