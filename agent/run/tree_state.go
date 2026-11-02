package run

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/Tangerg/scope/agent/internal/jsonwire"
)

// Records are the only input to this materialization. Scheduling, inspection and
// recovery share this fold so none can advance membership or progress alone.
type treeState struct {
	root             ProcessID
	limits           TreeLimits
	head             LSN
	epoch            Epoch
	startedProcesses uint64
	processes        map[ProcessID]*processState
	unsettledChild   ProcessID
}

func newTreeState(root ProcessID) *treeState {
	return &treeState{root: root, processes: make(map[ProcessID]*processState)}
}

func (t *treeState) apply(entry Entry) error {
	if entry.LSN != t.head+1 {
		return ErrInvalidLog
	}
	if err := t.applyRecord(entry.Record); err != nil {
		return fmt.Errorf("%w: record %d: %w", ErrInvalidLog, entry.LSN, err)
	}
	if t.epoch != entry.Epoch {
		return ErrInvalidLog
	}
	t.head = entry.LSN
	return nil
}

func (t *treeState) applyRecord(record Record) error {
	if t.unsettledChild.Valid() {
		settled, ok := record.(EffectSettled)
		if !ok || settled.Outcome.Status() != SettlementStatusSucceeded {
			return ErrInvalidLog
		}
	}
	switch value := record.(type) {
	case Started:
		return t.start(value)
	case Activated:
		if value.Epoch != t.epoch+1 {
			return ErrEpochConflict
		}
		t.epoch = value.Epoch
		return nil
	case SignalAdmitted:
		p, err := t.process(value.Process)
		if err != nil {
			return err
		}
		return p.admit(value.Signal, t.limits)
	case Stepped:
		p, err := t.process(value.Process)
		if err != nil {
			return err
		}
		return p.advance(value, t.limits)
	case EffectIntent:
		p, err := t.process(value.ID.Process)
		if err != nil {
			return err
		}
		return p.prepare(value)
	case EffectSettled:
		return t.settle(value)
	case Exited:
		return t.exit(value)
	case ExecutionFailed:
		p, err := t.process(value.Process)
		if err != nil {
			return err
		}
		if p.exited != nil || p.terminal.Valid() {
			return ErrInvalidLog
		}
		p.terminal = Termination{failure: value.Failure}
		p.execution = nil
		return nil
	case Reaped:
		return t.reap(value)
	case ControlRequested:
		return t.control(value)
	case Checkpoint:
		// A checkpoint materializes its own prefix; folding the prefix already
		// produced that state, so the record adds nothing to replay.
		return nil
	default:
		return ErrInvalidLog
	}
}

// verifyStep re-executes a committed Step against the restored Execution. A
// different Transition proves the Definition is not deterministic over its
// inputs, so continuing would diverge from what the log already promised.
func (t *treeState) verifyStep(ctx context.Context, stepped Stepped) error {
	p := t.processes[stepped.Process]
	if p == nil || p.execution == nil || int(stepped.Delivered) > len(p.inbox) {
		return ErrInvalidLog
	}
	turn := Turn{Process: stepped.Process, Step: stepped.Step, Messages: slices.Clone([]Message(p.inbox[:stepped.Delivered]))}
	transition, err := stepExecution(restoreContext{Context: ctx}, p.execution, turn)
	if err != nil || !transition.equal(stepped.Transition) {
		return fmt.Errorf("%w: process %s step %d", ErrNondeterministicStep, stepped.Process, stepped.Step)
	}
	return nil
}

func (t *treeState) process(id ProcessID) (*processState, error) {
	p := t.processes[id]
	if p == nil {
		return nil, ErrProcessNotFound
	}
	return p, nil
}

func (t *treeState) child(parent, id ProcessID) (*processState, error) {
	ancestor, ok := id.Parent()
	child := t.processes[id]
	if !ok || ancestor != parent || child == nil {
		return nil, ErrInvalidChildControl
	}
	return child, nil
}

func (t *treeState) settle(settled EffectSettled) error {
	process, err := t.process(settled.ID.Process)
	if err != nil {
		return err
	}
	pending, exists := process.pending[settled.ID]
	if !exists {
		return ErrEffectNotPending
	}
	effect := pending.Intent.Effect
	if effect.Target() == EffectTargetFramework {
		switch settled.Outcome.Status() {
		case SettlementStatusUnknown:
			return ErrInvalidLog
		case SettlementStatusSucceeded:
			if err := t.confirmFrameworkEffect(settled.ID.Process, effect, settled.Outcome); err != nil {
				return err
			}
		}
	}
	if t.unsettledChild.Valid() {
		return ErrInvalidLog
	}
	return process.settle(settled)
}

func (t *treeState) confirmFrameworkEffect(parent ProcessID, effect Effect, outcome Settlement) error {
	if spawn := effect.spawn; spawn != nil {
		child, err := jsonwire.Decode[ProcessID](outcome.payload)
		if err != nil || child != t.unsettledChild {
			return ErrInvalidChildStart
		}
		expected, err := parent.Child(spawn.Key)
		if err != nil || child != expected {
			return ErrInvalidChildStart
		}
		t.unsettledChild = ProcessID{}
		return nil
	}
	if _, err := jsonwire.Decode[struct{}](outcome.payload); err != nil {
		return ErrInvalidChildControl
	}
	if send := effect.send; send != nil {
		child, err := t.child(parent, send.Child)
		if err != nil {
			return err
		}
		receipt, exists := child.receipts[send.Signal.ID]
		if !exists || receipt.Digest != send.Signal.digest() {
			return ErrInvalidChildControl
		}
		return nil
	}
	if cancel := effect.cancel; cancel != nil {
		child, err := t.child(parent, cancel.Child)
		if err != nil {
			return err
		}
		if child.exited == nil && !child.terminal.Valid() {
			return ErrInvalidChildControl
		}
		return nil
	}
	return ErrInvalidEffect
}

func (t *treeState) start(started Started) error {
	if !started.valid() || started.Process.Root() != t.root || t.processes[started.Process] != nil {
		return ErrInvalidChildStart
	}
	if err := t.admitStart(started); err != nil {
		return err
	}
	if t.startedProcesses == math.MaxUint64 {
		return ErrCounterExhausted
	}
	if !t.limits.admitsTreeSize(t.startedProcesses + 1) {
		return ErrResourceLimitExceeded
	}
	t.startedProcesses++
	t.processes[started.Process] = newProcessState(started)
	return nil
}

func (t *treeState) admitStart(started Started) error {
	parentID, child := started.Process.Parent()
	if !child {
		if len(t.processes) != 0 {
			return ErrInvalidLog
		}
		t.limits = started.Limits
		return nil
	}
	parent, err := t.process(parentID)
	if err != nil {
		return err
	}
	spec := started.childSpec()
	if err := t.admitChild(parent, spec); err != nil {
		return err
	}
	if !parent.spawns(spec) {
		return ErrInvalidChildStart
	}
	if err := parent.adopt(spec); err != nil {
		return err
	}
	t.unsettledChild = started.Process
	return nil
}

func (t *treeState) exit(exited Exited) error {
	p, err := t.process(exited.Process)
	if err != nil {
		return err
	}
	if p.exited != nil || p.activeChildren != 0 || len(p.pending) != 0 || p.retainedChildren != 0 || !p.terminal.equal(exited.Termination) {
		return ErrInvalidLog
	}
	p.exited = &exited
	p.terminal = exited.Termination
	close(p.done)
	if parentID, child := exited.Process.Parent(); child {
		parent, err := t.process(parentID)
		if err != nil || parent.activeChildren == 0 {
			return ErrInvalidLog
		}
		if parent.usage.AcceptedMessages == math.MaxUint64 {
			return ErrCounterExhausted
		}
		parent.activeChildren--
		parent.inbox = append(parent.inbox, Exit{Child: exited.Process, Termination: exited.Termination})
		parent.yielded = false
		parent.usage.AcceptedMessages++
	}
	return nil
}

func (t *treeState) reap(reaped Reaped) error {
	child, err := t.process(reaped.Process)
	if err != nil || child.exited == nil {
		return ErrInvalidLog
	}
	parentID, ok := reaped.Process.Parent()
	if !ok {
		return ErrInvalidLog
	}
	parent, err := t.process(parentID)
	if err != nil || parent.retainedChildren == 0 {
		return ErrInvalidLog
	}
	for _, message := range parent.inbox {
		if exit, ok := message.(Exit); ok && exit.Child == reaped.Process && !parent.terminal.Valid() {
			return ErrInvalidLog
		}
	}
	if parent.terminal.Valid() {
		parent.inbox = slices.DeleteFunc(parent.inbox, func(message Message) bool { exit, ok := message.(Exit); return ok && exit.Child == reaped.Process })
	}
	parent.retainedChildren--
	delete(t.processes, reaped.Process)
	return nil
}

func (t *treeState) control(control ControlRequested) error {
	p, err := t.process(control.Process)
	if err != nil {
		return err
	}
	switch control.Kind {
	case ControlPause:
		if p.exited != nil || p.paused {
			return ErrInvalidProcessControl
		}
		p.paused = true
	case ControlResume:
		if p.exited != nil || !p.paused {
			return ErrInvalidProcessControl
		}
		p.paused = false
	case ControlCancel, ControlKill, ControlDeadline:
		for id, member := range t.processes {
			if !control.Process.Contains(id) || member.exited != nil {
				continue
			}
			cause := TerminationCauseHostCancellation
			if id != control.Process {
				cause = TerminationCauseParentCancellation
			}
			if control.Kind == ControlKill {
				cause = TerminationCauseRuntimeKill
			}
			if control.Kind == ControlDeadline {
				cause = TerminationCauseHostDeadline
				if id != control.Process {
					cause = TerminationCauseParentDeadline
				}
			}
			if member.terminal.accepts(cause) {
				member.terminal = Termination{cause: cause, reason: control.Reason}
			}
		}
	default:
		return ErrInvalidProcessControl
	}
	return nil
}

func (t *treeState) ordered() []ProcessID {
	ids := make([]ProcessID, 0, len(t.processes))
	for id := range t.processes {
		ids = append(ids, id)
	}
	slices.SortFunc(ids, func(a, b ProcessID) int { return strings.Compare(a.String(), b.String()) })
	return ids
}

func (t *treeState) checkpoint() (Checkpoint, error) {
	value := Checkpoint{StartedProcesses: t.startedProcesses, Processes: make([]ProcessCheckpoint, 0, len(t.processes))}
	for _, id := range t.ordered() {
		process, err := t.processes[id].checkpoint()
		if err != nil {
			return Checkpoint{}, err
		}
		value.Processes = append(value.Processes, process)
	}
	return value, nil
}

func projectLog(log Log) (*treeState, error) {
	state, rest, err := log.base()
	if err != nil {
		return nil, err
	}
	for _, entry := range rest {
		if err := state.apply(entry); err != nil {
			return nil, err
		}
	}
	if err := state.validateBoundary(); err != nil {
		return nil, err
	}
	return state, nil
}

// validateBoundary rejects a cut inside one batch: a child start without its
// settlement, or a Step without the intents of all its Effects.
func (t *treeState) validateBoundary() error {
	if t.unsettledChild.Valid() {
		return ErrInvalidLog
	}
	for _, process := range t.processes {
		if !process.intentsComplete() {
			return ErrInvalidLog
		}
	}
	return nil
}

func (t *treeState) inspect() TreeInspection {
	view := TreeInspection{Root: t.root, Head: t.head, Epoch: t.epoch}
	for _, id := range t.ordered() {
		p := t.processes[id]
		inspected := ProcessInspection{ID: id, Program: p.started.Program, Status: p.status(), Usage: p.usage, StartedAt: p.started.At}
		if p.exited != nil {
			inspected.Termination = p.exited.Termination
			inspected.FinishedAt = p.exited.At
		}
		for _, wait := range p.waits {
			inspected.Waits = append(inspected.Waits, wait)
		}
		slices.SortFunc(inspected.Waits, func(a, b Wait) int { return strings.Compare(a.Key.String(), b.Key.String()) })
		for _, pending := range p.pending {
			effect := PendingEffect{Request: EffectRequest{id: pending.Intent.ID, epoch: t.epoch, program: p.started.Program, effect: pending.Intent.Effect}, Policy: pending.Intent.Policy}
			if pending.Failure != nil {
				failure := *pending.Failure
				effect.Failure = &failure
			}
			if pending.Outcome != nil {
				effect.Outcome = *pending.Outcome
			}
			inspected.PendingEffects = append(inspected.PendingEffects, effect)
		}
		slices.SortFunc(inspected.PendingEffects, func(a, b PendingEffect) int { return strings.Compare(a.Request.ID().String(), b.Request.ID().String()) })
		view.Processes = append(view.Processes, inspected)
	}
	return view
}

func (t *treeState) admitChild(parent *processState, spec ChildSpec) error {
	if parent.exited != nil || parent.terminal.Valid() {
		return ErrInvalidChildStart
	}
	if parent.activeChildren == math.MaxUint32 || parent.retainedChildren == math.MaxUint32 {
		return ErrCounterExhausted
	}
	if spec.ProgramRef.Build() != parent.started.Program.Build() {
		return ErrInvalidProgram
	}
	child, err := parent.started.Process.Child(spec.Key)
	if err != nil {
		return err
	}
	if parent.adopted(spec.Key) {
		return ErrInvalidChildStart
	}
	if parent.childCount == math.MaxUint64 || t.startedProcesses == math.MaxUint64 {
		return ErrCounterExhausted
	}
	if !t.limits.admitsDepth(child.Depth()) || !t.limits.admitsChildren(parent.childCount+1, uint64(parent.activeChildren)+1) || !t.limits.admitsTreeSize(t.startedProcesses+1) {
		return ErrResourceLimitExceeded
	}
	if !parent.started.Budget.canAllocate(parent.usage, parent.allocated, spec.Budget) || !parent.started.Capabilities.Allows(spec.Capabilities) {
		return ErrResourceLimitExceeded
	}
	debit, ok := parent.started.Budget.allocation(spec.Budget)
	if !ok || !parent.started.Budget.Messages.Allows(parent.usage.AcceptedMessages, parent.allocated.Messages, debit.Messages, parent.reservedMessages(nil)) {
		return ErrResourceLimitExceeded
	}
	return nil
}
