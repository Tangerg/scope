package run

import (
	"context"
	"fmt"
	"math"
	"slices"
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
	if err := t.applyRecord(entry); err != nil {
		return fmt.Errorf("%w: record %d: %w", ErrInvalidLog, entry.LSN, err)
	}
	if t.epoch != entry.Epoch {
		return ErrInvalidLog
	}
	t.head = entry.LSN
	return nil
}

func (t *treeState) applyRecord(entry Entry) error {
	record := entry.Record
	if err := t.admitDuringChildStart(record); err != nil {
		return err
	}
	switch value := record.(type) {
	case Started:
		return t.start(value, entry.LSN)
	case Activated:
		return t.activate(value)
	case EffectSettled:
		return t.settle(value)
	case Exited:
		return t.exit(value)
	case Reaped:
		return t.reap(value)
	case ControlRequested:
		return t.control(value)
	case Checkpoint:
		// A checkpoint materializes its own prefix; folding the prefix already
		// produced that state, so the record adds nothing to replay.
		return nil
	case SignalAdmitted, Stepped, EffectIntent, ExecutionFailed:
		return t.applyToProcess(value)
	default:
		return ErrInvalidLog
	}
}

// admitDuringChildStart enforces that a child's Started is committed together
// with the settlement of the Spawn that requested it, and nothing else.
func (t *treeState) admitDuringChildStart(record Record) error {
	if !t.unsettledChild.Valid() {
		return nil
	}
	if settled, ok := record.(EffectSettled); !ok || settled.Outcome.Status() != SettlementStatusSucceeded {
		return ErrInvalidLog
	}
	return nil
}

func (t *treeState) activate(activated Activated) error {
	if activated.Epoch != t.epoch+1 {
		return ErrEpochConflict
	}
	t.epoch = activated.Epoch
	return nil
}

// applyToProcess applies a record that changes only the process it names.
func (t *treeState) applyToProcess(record Record) error {
	p, err := t.process(recordProcess(record))
	if err != nil {
		return err
	}
	switch value := record.(type) {
	case SignalAdmitted:
		return p.admit(value.Signal, t.limits)
	case Stepped:
		return p.advance(value, t.limits)
	case EffectIntent:
		return p.prepare(value)
	case ExecutionFailed:
		return p.failExecution(value.Failure)
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
		child, _, err := outcome.SpawnResult()
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
	if _, err := outcome.payload.Decode[struct{}](); err != nil {
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

func (t *treeState) start(started Started, lsn LSN) error {
	if !started.valid() || started.Process.Root() != t.root || t.processes[started.Process] != nil {
		return ErrInvalidChildStart
	}
	if err := t.admitStart(started, lsn); err != nil {
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

func (t *treeState) admitStart(started Started, lsn LSN) error {
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
	debit, err := t.admitChild(parent, spec)
	if err != nil {
		return err
	}
	if !parent.spawns(spec) {
		return ErrInvalidChildStart
	}
	if err := parent.adopt(spec.Key, debit, lsn); err != nil {
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
	if !p.acceptsControl(control.Kind) {
		return ErrInvalidProcessControl
	}
	switch control.Kind {
	case ControlPause:
		p.paused = true
	case ControlResume:
		p.paused = false
	default:
		t.terminate(control)
	}
	return nil
}

// terminate records the control's intent on the named process and every live
// descendant, unless a stronger intent is already recorded there.
func (t *treeState) terminate(control ControlRequested) {
	for id, member := range t.processes {
		if !control.Process.Contains(id) || member.exited != nil {
			continue
		}
		origin := control.Origin
		if id != control.Process {
			origin = ControlOriginParent
		}
		cause := control.Kind.terminationCause(origin)
		if member.terminal.accepts(cause) {
			member.terminate(Termination{cause: cause, reason: control.Reason})
		}
	}
}

// children lists parent's retained children in canonical order.
func (t *treeState) children(parent ProcessID) []ProcessID {
	var children []ProcessID
	for _, id := range t.ordered() {
		if owner, child := id.Parent(); child && owner == parent {
			children = append(children, id)
		}
	}
	return children
}

// restoreFromCheckpoint re-derives the membership counters a Checkpoint does
// not store, through the same grant rule live admission applies, and rejects
// a Checkpoint no sequence of admissions could have produced.
func (t *treeState) restoreFromCheckpoint(cut LSN) error {
	root := t.processes[t.root]
	if root == nil {
		return ErrInvalidLog
	}
	t.limits = root.started.Limits
	if !t.limits.admitsTreeSize(t.startedProcesses) {
		return ErrInvalidLog
	}
	granted, err := t.restoreChildren(cut)
	if err != nil {
		return err
	}
	for id, process := range t.processes {
		if !process.restoredConsistently(t.limits, granted[id]) || !t.checkpointExitsValid(process) {
			return ErrInvalidLog
		}
	}
	return nil
}

// restoreChildren returns what each parent has granted its retained children.
func (t *treeState) restoreChildren(cut LSN) (map[ProcessID]resourceAmounts, error) {
	claimedStarts := make(map[LSN]struct{})
	knownProcesses := uint64(1)
	granted := make(map[ProcessID]resourceAmounts)
	for id, process := range t.processes {
		if !t.limits.admitsDepth(id.Depth()) || !resourceQuantitiesFit(t.startedProcesses, knownProcesses, process.startedChildren()) {
			return nil, ErrInvalidLog
		}
		knownProcesses += process.startedChildren()
		for _, started := range process.children {
			if _, duplicate := claimedStarts[started]; duplicate || started >= cut {
				return nil, ErrInvalidLog
			}
			claimedStarts[started] = struct{}{}
		}
		parentID, child := id.Parent()
		if !child {
			continue
		}
		parent := t.processes[parentID]
		key, _ := id.Key()
		if parent == nil || parent.exited != nil || !parent.adopted(key) {
			return nil, ErrInvalidLog
		}
		debit, err := parent.childDebit(process.started.childSpec())
		if err != nil {
			return nil, ErrInvalidLog
		}
		total, fits := granted[parentID].add(debit)
		if !fits {
			return nil, ErrInvalidLog
		}
		granted[parentID] = total
		parent.retainedChildren++
		if process.exited == nil {
			parent.activeChildren++
		}
	}
	return granted, nil
}

func (t *treeState) checkpointExitsValid(process *processState) bool {
	var exits uint64
	for _, message := range process.inbox {
		if exit, ok := message.(Exit); ok {
			child := t.processes[exit.Child]
			if child == nil || child.exited == nil || !exit.Termination.equal(child.exited.Termination) {
				return false
			}
			exits++
		}
	}
	return exits == uint64(process.retainedChildren-process.activeChildren)
}

func (t *treeState) ordered() []ProcessID {
	ids := make([]ProcessID, 0, len(t.processes))
	for id := range t.processes {
		ids = append(ids, id)
	}
	slices.SortFunc(ids, ProcessID.compare)
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

// reapedSubtree finds the reaped ancestor-or-self of process whose parent is
// still live, with the LSN at which that parent started it.
func (t *treeState) reapedSubtree(process ProcessID) (ProcessID, LSN, bool) {
	for subtree := process; ; {
		parentID, child := subtree.Parent()
		if !child {
			return ProcessID{}, 0, false
		}
		if parent := t.processes[parentID]; parent != nil {
			key, _ := subtree.Key()
			started, adopted := parent.children[key]
			return subtree, started, adopted
		}
		subtree = parentID
	}
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
		inspected.Waits = p.orderedWaits()
		for _, pending := range p.orderedPending() {
			inspected.PendingEffects = append(inspected.PendingEffects, pending.inspect(t.epoch, p.started.Program))
		}
		view.Processes = append(view.Processes, inspected)
	}
	return view
}

// admitChild applies the live half of child admission on top of the grant the
// parent can make, and returns that grant for the parent to debit.
func (t *treeState) admitChild(parent *processState, spec ChildSpec) (resourceAmounts, error) {
	if parent.exited != nil || parent.terminal.Valid() {
		return resourceAmounts{}, ErrInvalidChildStart
	}
	if parent.activeChildren == math.MaxUint32 || parent.retainedChildren == math.MaxUint32 {
		return resourceAmounts{}, ErrCounterExhausted
	}
	debit, err := parent.childDebit(spec)
	if err != nil {
		return resourceAmounts{}, err
	}
	child, err := parent.started.Process.Child(spec.Key)
	if err != nil {
		return resourceAmounts{}, err
	}
	if parent.adopted(spec.Key) {
		return resourceAmounts{}, ErrInvalidChildStart
	}
	if t.startedProcesses == math.MaxUint64 {
		return resourceAmounts{}, ErrCounterExhausted
	}
	if !t.limits.admitsDepth(child.Depth()) || !t.limits.admitsChildren(parent.startedChildren()+1, uint64(parent.activeChildren)+1) || !t.limits.admitsTreeSize(t.startedProcesses+1) {
		return resourceAmounts{}, ErrResourceLimitExceeded
	}
	if !parent.started.Budget.canAllocate(parent.usage, parent.allocated, spec.Budget) || !parent.started.Budget.Messages.Allows(parent.usage.AcceptedMessages, parent.allocated.Messages, debit.Messages, parent.reservedMessages(nil)) {
		return resourceAmounts{}, ErrResourceLimitExceeded
	}
	return debit, nil
}
