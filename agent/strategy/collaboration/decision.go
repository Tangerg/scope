package collaboration

import agent "github.com/Tangerg/scope/agent"

const invalidEnumName = "invalid"

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

func (m Mode) Valid() bool {
	return m == ModeUndecided || m == ModeContinue || m == ModeWait
}

func (m Mode) String() string {
	if !m.Valid() {
		return invalidEnumName
	}
	if m == ModeUndecided {
		return "undecided"
	}
	return string(m)
}

// TaskRequest starts a new bounded execution of a configured worker. To follow
// up on a completed task, choose a new Key and carry the previous result in
// Input. Terminal Processes are immutable; continuation has a new lifecycle.
type TaskRequest struct {
	Key    agent.ChildKey `json:"key"`
	Worker string         `json:"worker"`
	Input  agent.Payload  `json:"input"`
}

// Task retains one request and its canonical kernel lifecycle facts. A task
// with neither Start nor Outcome is pending admission; Start alone is a failed
// admission or an outstanding child; Outcome replaces Start once the child
// drains, and its Result names the child.
type Task struct {
	Request TaskRequest             `json:"request"`
	Start   *agent.ChildStartResult `json:"start,omitzero"`
	Outcome *agent.ChildOutcome     `json:"outcome,omitzero"`
}

// processID names the task's child while it runs or after it finished.
func (t Task) processID() (agent.ProcessID, bool) {
	if t.Outcome != nil {
		return t.Outcome.Result().ProcessID(), true
	}
	if t.Start != nil {
		return t.Start.ProcessID()
	}
	return agent.ProcessID{}, false
}

// Control targets an already admitted task. Exactly one of Signal and
// CancelReason is present. Signal uses the recipient's own input contract and
// caller-stable identity; cancellation is intent, not proof of resource release.
type Control struct {
	Task         agent.ChildKey       `json:"task"`
	Signal       *agent.SignalRequest `json:"signal,omitzero"`
	CancelReason *string              `json:"cancel_reason,omitzero"`
}

type ControlReceipt struct {
	Control Control                   `json:"control"`
	Result  *agent.ChildControlResult `json:"result,omitzero"`
}

// Turn is the coordinator's complete portable input. Tasks are cumulative and
// request ordered. Controls contains the previous decision's action receipts.
// State is explicit working memory; a Decision replaces it in full. Workers
// exposes the exact configured input/output contracts without execution grants.
// Facts arriving while this turn runs are visible in the following turn.
type Turn struct {
	Number   uint64             `json:"number"`
	State    agent.Payload      `json:"state"`
	Workers  []agent.Descriptor `json:"workers"`
	Tasks    []Task             `json:"tasks"`
	Controls []ControlReceipt   `json:"controls"`
}

// Decision is the coordinator's output contract. The entire batch is validated
// before any action is declared. A present Output completes the collaboration
// and cancels unfinished descendants; it admits no Mode or actions. Otherwise
// Mode chooses how the next turn starts. An explicit JSON null is a present
// Output. Input and Output retain the configured domain schemas.
type Decision struct {
	Mode     Mode          `json:"mode"`
	State    agent.Payload `json:"state"`
	Tasks    []TaskRequest `json:"tasks,omitempty"`
	Controls []Control     `json:"controls,omitempty"`
	Output   agent.Payload `json:"output,omitzero"`
}

func (d Decision) completes() bool { return d.Output.Valid() }

// decided distinguishes a coordinator's Decision from the zero value a turn
// holds before its coordinator answers.
func (d Decision) decided() bool { return d.completes() || d.Mode != ModeUndecided }

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

func (c Control) effect(task *Task) (agent.Effect, error) {
	if task == nil || task.Request.Key != c.Task || (c.Signal == nil) == (c.CancelReason == nil) {
		return agent.Effect{}, ErrInvalidDecision
	}
	id, started := task.processID()
	if !started {
		return agent.Effect{}, ErrInvalidDecision
	}
	if c.Signal != nil {
		return agent.NewChildSignalEffect(id, *c.Signal)
	}
	return agent.NewChildCancelEffect(id, *c.CancelReason)
}
