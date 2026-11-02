package child

import (
	"errors"
	"fmt"

	"github.com/Tangerg/scope/agent/run"
)

var ErrInvalidMessage = errors.New("child: message does not answer the pending invocation")

// Invocation retains only the outstanding obligation. The runtime owns child
// membership; replacing one variant discards facts the next variant supersedes.
type Invocation struct {
	Request     *run.EffectID    `json:"request,omitzero"`
	Process     *run.ProcessID   `json:"process,omitzero"`
	Termination *run.Termination `json:"termination,omitzero"`
	Rejection   *run.Failure     `json:"rejection,omitzero"`
}

func Start(turn run.Turn, index uint32, spec run.ChildSpec) (Invocation, run.Effect, error) {
	effect, err := run.NewChildStartEffect(spec)
	if err != nil {
		return Invocation{}, run.Effect{}, err
	}
	id := turn.EffectID(index)
	if !id.Valid() {
		return Invocation{}, run.Effect{}, ErrInvalidMessage
	}
	return Invocation{Request: &id}, effect, nil
}

func (i Invocation) Valid() bool {
	count := 0
	if i.Request != nil {
		if !i.Request.Valid() {
			return false
		}
		count++
	}
	if i.Process != nil {
		_, hasParent := i.Process.Parent()
		if !i.Process.Valid() || !hasParent {
			return false
		}
		count++
	}
	if i.Termination != nil {
		if !i.Termination.Valid() {
			return false
		}
		count++
	}
	if i.Rejection != nil {
		if !i.Rejection.Valid() {
			return false
		}
		count++
	}
	return count == 1
}

func (i Invocation) Done() bool { return i.Termination != nil || i.Rejection != nil }

func (i *Invocation) Accept(message run.Message) (bool, error) {
	if i == nil || !i.Valid() {
		return false, ErrInvalidMessage
	}
	switch value := message.(type) {
	case run.Completion:
		if i.Request == nil || value.Effect != *i.Request {
			return false, nil
		}
		payload := value.Outcome.Payload()
		switch value.Outcome.Status() {
		case run.SettlementStatusSucceeded:
			process, err := payload.Decode[run.ProcessID]()
			if err != nil {
				return false, err
			}
			parent, child := process.Parent()
			if !child || parent != i.Request.Process {
				return false, ErrInvalidMessage
			}
			*i = Invocation{Process: &process}
		case run.SettlementStatusFailed:
			failure, err := payload.Decode[run.Failure]()
			if err != nil || !failure.Valid() {
				return false, fmt.Errorf("%w: invalid spawn failure", ErrInvalidMessage)
			}
			*i = Invocation{Rejection: &failure}
		default:
			return false, ErrInvalidMessage
		}
		return true, nil
	case run.Exit:
		if i.Process == nil || value.Child != *i.Process {
			return false, nil
		}
		if !value.Termination.Valid() {
			return false, ErrInvalidMessage
		}
		*i = Invocation{Termination: &value.Termination}
		return true, nil
	default:
		return false, nil
	}
}
