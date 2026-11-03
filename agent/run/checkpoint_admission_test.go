package run

import (
	"errors"
	"testing"
)

func TestCheckpointRejectsConflictingProcessFactsBeforeRecovery(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*ProcessCheckpoint)
	}{
		{"duplicate wait", func(p *ProcessCheckpoint) { p.Waits = []Wait{kernelWait(), kernelWait()} }},
		{"duplicate receipt", func(p *ProcessCheckpoint) {
			signal := Signal{ID: controlValue(ParseSignalID("receipt")), Payload: p.Started.Input}
			p.Receipts = []SignalReceipt{signal.receipt(), signal.receipt()}
			p.Usage.AcceptedMessages = 2
		}},
		{"answered wait", func(p *ProcessCheckpoint) {
			wait := kernelWait()
			p.Waits = []Wait{wait}
			p.Receipts = []SignalReceipt{(Signal{ID: controlValue(ParseSignalID("receipt")), Wait: wait.Key, Payload: p.Started.Input}).receipt()}
			p.Usage.AcceptedMessages = 1
		}},
		{"two answers for one wait", func(p *ProcessCheckpoint) {
			for _, id := range []string{"first", "second"} {
				p.Receipts = append(p.Receipts, (Signal{ID: controlValue(ParseSignalID(id)), Wait: kernelWait().Key, Payload: p.Started.Input}).receipt())
			}
			p.Usage.AcceptedMessages = 2
		}},
		{"signal without receipt", func(p *ProcessCheckpoint) {
			p.Inbox = Inbox{Signal{ID: controlValue(ParseSignalID("receipt")), Payload: p.Started.Input}}
			p.Usage.AcceptedMessages = 1
		}},
		{"signal disagrees with receipt", func(p *ProcessCheckpoint) {
			signal := Signal{ID: controlValue(ParseSignalID("receipt")), Payload: p.Started.Input}
			p.Receipts = []SignalReceipt{signal.receipt()}
			signal.Payload = controlValue(EncodePayload("different"))
			p.Inbox, p.Usage.AcceptedMessages = Inbox{signal}, 1
		}},
		{"duplicate inbox signal", func(p *ProcessCheckpoint) {
			signal := Signal{ID: controlValue(ParseSignalID("receipt")), Payload: p.Started.Input}
			p.Receipts = []SignalReceipt{signal.receipt()}
			p.Inbox, p.Usage.AcceptedMessages = Inbox{signal, signal}, 2
		}},
		{"foreign completion owner", func(p *ProcessCheckpoint) {
			pending := checkpointPending(p)
			pending.Intent.ID.Process = controlValue(ParseProcessID("foreign"))
			p.Inbox = Inbox{Completion{Effect: pending.Intent.ID, Outcome: controlValue(NewSettlement(SettlementStatusSucceeded, []byte(`null`)))}}
			p.Usage.AcceptedMessages = 1
		}},
		{"duplicate inbox completion", func(p *ProcessCheckpoint) {
			pending := checkpointPending(p)
			completion := Completion{Effect: pending.Intent.ID, Outcome: controlValue(NewSettlement(SettlementStatusSucceeded, []byte(`null`)))}
			p.Inbox, p.Usage.AcceptedMessages = Inbox{completion, completion}, 2
		}},
		{"foreign exit owner", func(p *ProcessCheckpoint) {
			p.Inbox = Inbox{Exit{Child: controlValue(ParseProcessID("foreign/child")), Termination: Termination{output: p.Started.Input}}}
			p.Usage.AcceptedMessages = 1
		}},
		{"missing inbox message", func(p *ProcessCheckpoint) { p.Inbox, p.Usage.AcceptedMessages = Inbox{nil}, 1 }},
		{"pending without intent", func(p *ProcessCheckpoint) { p.Pending = []PendingCheckpoint{{}} }},
		{"foreign pending owner", func(p *ProcessCheckpoint) {
			pending := checkpointPending(p)
			pending.Intent.ID.Process = controlValue(ParseProcessID("foreign"))
			p.Pending = []PendingCheckpoint{pending}
		}},
		{"future effect step", func(p *ProcessCheckpoint) {
			pending := checkpointPending(p)
			pending.Intent.ID.Step++
			p.Pending = []PendingCheckpoint{pending}
		}},
		{"duplicate pending effect", func(p *ProcessCheckpoint) {
			pending := checkpointPending(p)
			p.Usage.PreparedEffects = 2
			p.Pending = []PendingCheckpoint{pending, pending}
		}},
		{"successful settlement pending", func(p *ProcessCheckpoint) {
			pending := checkpointPending(p)
			pending.Outcome = new(controlValue(NewSettlement(SettlementStatusSucceeded, []byte(`null`))))
			p.Pending = []PendingCheckpoint{pending}
		}},
		{"ungranted effect capability", func(p *ProcessCheckpoint) {
			pending := checkpointPending(p)
			pending.Intent.Policy.RequiredCapabilities = controlValue(NewCapabilitySet(controlValue(ParseCapability("test.external"))))
			p.Pending = []PendingCheckpoint{pending}
		}},
		{"pending completion", func(p *ProcessCheckpoint) {
			pending := checkpointPending(p)
			p.Pending = []PendingCheckpoint{pending}
			p.Inbox = Inbox{Completion{Effect: pending.Intent.ID, Outcome: controlValue(NewSettlement(SettlementStatusSucceeded, []byte(`null`)))}}
			p.Usage.AcceptedMessages = 1
		}},
		{"used step exceeds grant", func(p *ProcessCheckpoint) {
			p.Started.Budget.Steps = NewQuota(1)
			p.Usage.CommittedSteps = 2
		}},
		{"unlimited grant carries finite debit", func(p *ProcessCheckpoint) { p.Allocated.Steps = 1 }},
		{"inbox exceeds admission capacity", func(p *ProcessCheckpoint) {
			p.Started.Limits.MaxPendingMessages = 1
			for _, id := range []string{"first", "second"} {
				signal := Signal{ID: controlValue(ParseSignalID(id)), Payload: p.Started.Input}
				p.Receipts = append(p.Receipts, signal.receipt())
				p.Inbox = append(p.Inbox, signal)
			}
			p.Usage.AcceptedMessages = 2
		}},
		{"terminal wait", func(p *ProcessCheckpoint) {
			p.Terminal = &Termination{cause: TerminationCauseHostCancellation, reason: "canceled"}
			p.State, p.Waits = ExecutionState{}, []Wait{kernelWait()}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			log := integrityInitialLog(t)
			started := log.Entries[0].Record.(Started)
			checkpoint := Checkpoint{StartedProcesses: 1, Processes: []ProcessCheckpoint{{Started: started, State: controlValue(ParseExecutionState("fixture", started.Input.JSON()))}}}
			test.change(&checkpoint.Processes[0])
			assertCheckpointRejected(t, log.Root, checkpoint)
		})
	}
}

func TestCheckpointRetainsExactlyOneMatchingExitUntilItsParentConsumesIt(t *testing.T) {
	log := integrityInitialLog(t)
	parent := log.Entries[0].Record.(Started)
	key := controlValue(ParseChildKey("child"))
	child := parent
	child.Process, child.Limits = controlValue(parent.Process.Child(key)), TreeLimits{}
	terminal := Termination{output: child.Input}
	exit := Exit{Child: child.Process, Termination: terminal}
	checkpoint := Checkpoint{StartedProcesses: 2, Processes: []ProcessCheckpoint{
		{Started: parent, State: controlValue(ParseExecutionState("fixture", parent.Input.JSON())), Children: []StartedChild{{Key: key, Started: 5}},
			Inbox: Inbox{exit}, Usage: Usage{CommittedSteps: 1, PreparedEffects: 1, AcceptedMessages: 2}},
		{Started: child, Terminal: &terminal, Exited: &Exited{Process: child.Process, Termination: terminal, At: child.At}},
	}}
	cut := Log{Root: log.Root, Entries: []Entry{{LSN: 20, Epoch: 1, Record: checkpoint}}}
	if err := cut.Validate(); err != nil {
		t.Fatalf("matching retained exit rejected: %v", err)
	}
	for _, test := range []struct {
		name   string
		change func(*Checkpoint)
	}{
		{"duplicate exit", func(c *Checkpoint) {
			c.Processes[0].Inbox = append(c.Processes[0].Inbox, exit)
			c.Processes[0].Usage.AcceptedMessages++
		}},
		{"changed termination", func(c *Checkpoint) {
			c.Processes[0].Inbox = Inbox{Exit{Child: child.Process, Termination: Termination{output: controlValue(EncodePayload("changed"))}}}
		}},
		{"exited child still owns an effect", func(c *Checkpoint) {
			pending := checkpointPending(&c.Processes[1])
			c.Processes[1].Pending = []PendingCheckpoint{pending}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := checkpoint.clone()
			test.change(&changed)
			assertCheckpointRejected(t, log.Root, changed)
		})
	}
}

func checkpointPending(p *ProcessCheckpoint) PendingCheckpoint {
	p.Usage.CommittedSteps, p.Usage.PreparedEffects = 1, 1
	return PendingCheckpoint{Intent: EffectIntent{
		ID:     EffectID{Process: p.Started.Process, Step: 1},
		Effect: controlValue(NewDispatcherEffect([]byte(`null`))),
		Policy: EffectPolicy{Replay: ReplayPolicyNever}, At: p.Started.At,
	}}
}

func assertCheckpointRejected(t *testing.T, root ProcessID, checkpoint Checkpoint) {
	t.Helper()
	cut := Log{Root: root, Entries: []Entry{{LSN: 20, Epoch: 1, Record: checkpoint}}}
	if err := cut.Validate(); !errors.Is(err, ErrInvalidLog) {
		t.Fatalf("invalid checkpoint admitted: %v", err)
	}
	store := &integrityStore{log: cut}
	runtime, program := integrityRuntime(t, store)
	if _, err := runtime.Recover(t.Context(), program, root); !errors.Is(err, ErrInvalidLog) || store.appends != 0 {
		t.Fatalf("invalid checkpoint reached writer acquisition: error=%v appends=%d", err, store.appends)
	}
}

func TestCheckpointRejectsConflictingTreeFacts(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*Checkpoint)
	}{
		{"unordered members", func(c *Checkpoint) { c.Processes[0], c.Processes[1] = c.Processes[1], c.Processes[0] }},
		{"too many started processes", func(c *Checkpoint) { c.Processes[0].Started.Limits.MaxTreeProcesses = NewQuota(1) }},
		{"claimed start beyond checkpoint", func(c *Checkpoint) { c.Processes[0].Children[0].Started = 20 }},
		{"claimed starts exceed count", func(c *Checkpoint) {
			c.Processes[0].Children = append(c.Processes[0].Children, StartedChild{Key: controlValue(ParseChildKey("reaped")), Started: 7})
		}},
		{"child capability exceeds parent", func(c *Checkpoint) {
			c.Processes[1].Started.Capabilities = controlValue(NewCapabilitySet(controlValue(ParseCapability("test.external"))))
		}},
		{"unlimited child below finite grant", func(c *Checkpoint) { c.Processes[0].Started.Budget.Steps = NewQuota(5) }},
		{"missing retained allocation", func(c *Checkpoint) {
			c.Processes[0].Started.Budget.Steps, c.Processes[1].Started.Budget.Steps = NewQuota(5), NewQuota(2)
		}},
		{"exit for a live child", func(c *Checkpoint) {
			c.Processes[0].Inbox = Inbox{Exit{Child: c.Processes[1].Started.Process, Termination: Termination{output: c.Processes[1].Started.Input}}}
			c.Processes[0].Usage.AcceptedMessages = 1
		}},
		{"exited child lacks its parent message", func(c *Checkpoint) {
			child := &c.Processes[1]
			child.State, child.Terminal = ExecutionState{}, &Termination{output: child.Started.Input}
			child.Exited = &Exited{Process: child.Started.Process, Termination: *child.Terminal, At: child.Started.At}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			log := integrityInitialLog(t)
			parent := log.Entries[0].Record.(Started)
			key := controlValue(ParseChildKey("child"))
			child := parent
			child.Process, child.Limits = controlValue(parent.Process.Child(key)), TreeLimits{}
			checkpoint := Checkpoint{StartedProcesses: 2, Processes: []ProcessCheckpoint{
				{Started: parent, State: controlValue(ParseExecutionState("fixture", parent.Input.JSON())), Children: []StartedChild{{Key: key, Started: 5}}, Usage: Usage{CommittedSteps: 1, PreparedEffects: 1, AcceptedMessages: 1}},
				{Started: child, State: controlValue(ParseExecutionState("fixture", child.Input.JSON()))},
			}}
			if err := (Log{Root: log.Root, Entries: []Entry{{LSN: 20, Epoch: 1, Record: checkpoint}}}).Validate(); err != nil {
				t.Fatalf("valid checkpoint baseline: %v", err)
			}
			test.change(&checkpoint)
			assertCheckpointRejected(t, log.Root, checkpoint)
		})
	}
}

func TestCheckpointUsageOwnsTheNextStepIdentity(t *testing.T) {
	log := integrityInitialLog(t)
	started := log.Entries[0].Record.(Started)
	checkpoint := Checkpoint{StartedProcesses: 1, Processes: []ProcessCheckpoint{{
		Started: started, State: controlValue(ParseExecutionState("fixture", started.Input.JSON())), Usage: Usage{CommittedSteps: 7},
	}}}
	state := controlValue(checkpoint.tree(log.Root, Entry{LSN: 20, Epoch: 1, Record: checkpoint}))
	process := state.processes[log.Root]
	step := Stepped{Process: log.Root, Step: 8, Transition: controlValue(Yield(0, nil, kernelWait()))}
	if err := process.advance(step, state.limits); err != nil || process.usage.CommittedSteps != 8 {
		t.Fatalf("recovered usage did not own step admission: usage=%+v error=%v", process.usage, err)
	}
}
