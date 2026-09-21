package collaboration

import agent "github.com/Tangerg/scope/agent"

const invalidEnumName = "invalid"

// Mode chooses when the next coordinator turn runs.
type Mode string

const (
	// Undecided records the absence of a coordinator decision.
	Undecided Mode = ""
	// Continue starts the next turn after action receipts, while tasks run.
	Continue Mode = "continue"
	// Wait starts the next turn after at least one outstanding task drains.
	// If every attempted start failed, their receipts trigger the next turn.
	Wait Mode = "wait"
	// Complete ends this collaboration and cancels unfinished descendants.
	Complete Mode = "complete"
)

func (m Mode) Valid() bool { return m == Undecided || m == Continue || m == Wait || m == Complete }

func (m Mode) String() string {
	if !m.Valid() {
		return invalidEnumName
	}
	if m == Undecided {
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

// Task retains one request and its canonical kernel lifecycle facts. A missing
// Start is pending admission; a successful Start without Outcome is outstanding.
type Task struct {
	Request TaskRequest             `json:"request"`
	Start   *agent.ChildStartResult `json:"start,omitzero"`
	Outcome *agent.ChildOutcome     `json:"outcome,omitzero"`
}

// Control targets an already admitted task. Exactly one of Signal and
// CancelReason is present. Signal uses the recipient's own input contract and
// caller-stable identity; cancellation is intent, not proof of resource release.
type Control struct {
	Task         agent.ChildKey       `json:"task"`
	Signal       *agent.SignalRequest `json:"signal,omitzero"`
	CancelReason *string              `json:"cancel_reason,omitzero"`
}

// ControlReceipt preserves both the declared action and its admission result.
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
// before any action is declared. Complete requires Output and no actions; other
// modes require a zero Output. An explicit JSON null is a present Output.
// Input and Output retain the configured domain schemas.
type Decision struct {
	Mode     Mode          `json:"mode"`
	State    agent.Payload `json:"state"`
	Tasks    []TaskRequest `json:"tasks,omitempty"`
	Controls []Control     `json:"controls,omitempty"`
	Output   agent.Payload `json:"output,omitzero"`
}

func (c Control) effect(task *Task) (agent.Effect, error) {
	if task == nil || task.Request.Key != c.Task || task.Start == nil || (c.Signal == nil) == (c.CancelReason == nil) {
		return agent.Effect{}, ErrInvalidDecision
	}
	id, started := task.Start.ProcessID()
	if !started {
		return agent.Effect{}, ErrInvalidDecision
	}
	if c.Signal != nil {
		return agent.NewChildSignalEffect(id, *c.Signal)
	}
	return agent.NewChildCancelEffect(id, *c.CancelReason)
}
