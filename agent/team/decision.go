package team

import (
	"github.com/Tangerg/scope/agent/internal/enum"
	"github.com/Tangerg/scope/agent/run"
)

type Mode string

const (
	ModeUndecided Mode = ""
	// ModeContinue starts the next turn after action receipts, while tasks run.
	ModeContinue Mode = "continue"
	// ModeWait starts the next turn after at least one outstanding task drains.
	// If no tasks remain outstanding, failed-start receipts trigger the next turn.
	// Older outstanding tasks still delay the turn when every new start fails;
	// use ModeContinue to process action receipts without waiting for those tasks.
	ModeWait Mode = "wait"
)

const (
	modeUndecidedName = "undecided"
)

func (m Mode) Valid() bool {
	return m == ModeUndecided || m == ModeContinue || m == ModeWait
}

// JSONSchemaEnum publishes the decided modes. A completing Decision omits the
// member, so the undecided zero value never appears on the wire.
func (Mode) JSONSchemaEnum() []string { return []string{string(ModeContinue), string(ModeWait)} }

func (m Mode) String() string {
	if m == ModeUndecided {
		return modeUndecidedName
	}
	return enum.String(m)
}

// TaskRequest starts a new bounded execution of a configured worker. To follow
// up on a completed task, choose a new Key and carry the previous result in
// Input. Terminal Processes are immutable; continuation has a new lifecycle.
type TaskRequest struct {
	Key    run.ChildKey `json:"key"`
	Worker string       `json:"worker"`
	Input  run.Payload  `json:"input"`
}

type Task struct {
	Request   TaskRequest      `json:"request"`
	Process   *run.ProcessID   `json:"process,omitzero"`
	Outcome   *run.Termination `json:"outcome,omitzero"`
	Rejection *run.Failure     `json:"rejection,omitzero"`
}

// Control targets an already admitted task. Exactly one of Signal and
// CancelReason is present. Signal uses the recipient's own input contract and
// caller-stable identity; cancellation is intent, not proof of resource release.
// A task may exit during the coordinator turn; the runtime then decides the
// control's receipt without invalidating the coordinator's decision.
type Control struct {
	Task         run.ChildKey `json:"task"`
	Signal       *run.Signal  `json:"signal,omitzero"`
	CancelReason *string      `json:"cancel_reason,omitzero"`
}

type ControlReceipt struct {
	Control Control         `json:"control"`
	Result  *run.Settlement `json:"result,omitzero"`
}

// Turn is the coordinator's complete portable input. Tasks are cumulative and
// request ordered. Controls contains the previous decision's action receipts.
// State is explicit working memory; a Decision replaces it in full. Workers
// exposes the exact configured input/output contracts without execution grants.
// Facts arriving while this turn runs are visible in the following turn.
type Turn struct {
	Number   uint64           `json:"number"`
	State    run.Payload      `json:"state"`
	Workers  []run.Descriptor `json:"workers"`
	Tasks    []Task           `json:"tasks"`
	Controls []ControlReceipt `json:"controls"`
}

// Decision is the coordinator's output contract. The entire batch is validated
// before any action is declared. A present Output completes the collaboration
// and cancels unfinished descendants; it admits no Mode or actions. Otherwise
// Mode chooses how the next turn starts. An explicit JSON null is a present
// Output. Input and Output retain the configured domain schemas.
type Decision struct {
	Mode     Mode          `json:"mode,omitzero"`
	State    run.Payload   `json:"state"`
	Tasks    []TaskRequest `json:"tasks,omitempty"`
	Controls []Control     `json:"controls,omitempty"`
	Output   run.Payload   `json:"output,omitzero"`
}

func (d Decision) completes() bool { return d.Output.Valid() }

func (d Decision) validateShape() error {
	if d.completes() {
		if d.Mode != ModeUndecided || len(d.Tasks) != 0 || len(d.Controls) != 0 {
			return ErrInvalidDecision
		}
		return nil
	}
	if d.Mode != ModeContinue && d.Mode != ModeWait {
		return ErrInvalidDecision
	}
	return nil
}

func (c Control) validate() error {
	if !c.Task.Valid() || (c.Signal == nil) == (c.CancelReason == nil) {
		return ErrInvalidDecision
	}
	if c.Signal != nil && !c.Signal.Valid() {
		return ErrInvalidDecision
	}
	if c.CancelReason != nil && !run.ValidTerminationReason(*c.CancelReason) {
		return ErrInvalidDecision
	}
	return nil
}

func (c Control) effect(parent run.ProcessID, task *taskState) (run.Effect, error) {
	if task == nil || c.validate() != nil {
		return run.Effect{}, ErrInvalidDecision
	}
	id, started := task.processID(parent)
	if !started {
		return run.Effect{}, ErrInvalidDecision
	}
	if c.Signal != nil {
		return run.NewChildSignalEffect(id, *c.Signal)
	}
	return run.NewChildCancelEffect(id, *c.CancelReason)
}
