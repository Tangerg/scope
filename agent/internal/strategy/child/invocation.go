package child

import (
	"errors"
	"fmt"

	"github.com/Tangerg/scope/agent/internal/jsonwire"

	"github.com/Tangerg/scope/agent/run"
)

// ErrInvalidMessage is a child protocol violation: a message that answers the
// pending invocation but cannot be applied to it. It carries one Failure
// classification, so every strategy reports the violation identically
// whether or not the strategy adds its own context.
var ErrInvalidMessage = run.NewClassifiedError(run.FailureKindContract, "strategy.child.message_invalid", "child: message does not answer the pending invocation")

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
		return Invocation{}, run.Effect{}, fmt.Errorf("%w: turn cannot address a child start", run.ErrInvalidIdentity)
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

// UnmarshalJSON admits only an invocation in exactly one state, so a restored
// strategy can never hold a contradictory obligation.
func (i *Invocation) UnmarshalJSON(data []byte) error {
	type wire Invocation
	decoded, err := jsonwire.Decode[wire](data)
	if err != nil {
		return err
	}
	if !Invocation(decoded).Valid() {
		return errors.New("child: invocation must hold exactly one valid obligation")
	}
	*i = Invocation(decoded)
	return nil
}

func (i Invocation) Done() bool { return i.Termination != nil || i.Rejection != nil }

// Accept applies message to the invocation it answers. Start and decoding are
// the only sources of an Invocation, and both leave exactly one obligation.
func (i *Invocation) Accept(message run.Message) (bool, error) {
	switch value := message.(type) {
	case run.Completion:
		if i.Request == nil || value.Effect != *i.Request {
			return false, nil
		}
		child, rejection, err := value.Outcome.SpawnResult()
		if err != nil {
			return false, fmt.Errorf("%w: %w", ErrInvalidMessage, err)
		}
		if !child.Valid() {
			*i = Invocation{Rejection: &rejection}
			return true, nil
		}
		if parent, _ := child.Parent(); parent != i.Request.Process {
			return false, ErrInvalidMessage
		}
		*i = Invocation{Process: &child}
		return true, nil
	case run.Exit:
		if i.Process == nil || value.Child != *i.Process {
			return false, nil
		}
		*i = Invocation{Termination: &value.Termination}
		return true, nil
	default:
		return false, nil
	}
}

// Batch is the invocations one Step declared, in declaration order.
type Batch []Invocation

// Parent checks the batch as one declaration: each unanswered Spawn sits at its
// own Effect index within one Step, each started child carries the key
// expected at its position, and all share one parent. It returns that parent,
// or zero when no invocation has reached the Runtime yet.
func (b Batch) Parent(expectedKey func(position int) (run.ChildKey, error)) (run.ProcessID, error) {
	var parent run.ProcessID
	var step uint64
	for position, invocation := range b {
		if !invocation.Valid() {
			return run.ProcessID{}, errInvalidBatch
		}
		var current run.ProcessID
		switch {
		case invocation.Request != nil:
			if invocation.Request.Index != uint32(position) || step != 0 && invocation.Request.Step != step {
				return run.ProcessID{}, errInvalidBatch
			}
			current, step = invocation.Request.Process, invocation.Request.Step
		case invocation.Process != nil:
			key, _ := invocation.Process.Key()
			expected, err := expectedKey(position)
			if err != nil || key != expected {
				return run.ProcessID{}, errInvalidBatch
			}
			current, _ = invocation.Process.Parent()
		default:
			continue
		}
		if parent.Valid() && parent != current {
			return run.ProcessID{}, errInvalidBatch
		}
		parent = current
	}
	return parent, nil
}

var errInvalidBatch = errors.New("child: invocations do not form one declared batch")
