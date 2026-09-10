package agent

import "errors"

func (t *treeRuntime) applyCommand(command treeCommand) {
	switch command.kind {
	case treeCommandAcquireFreeze:
		t.acquireFreeze(command.acquisition)
		return
	case treeCommandReleaseFreeze:
		err := t.releaseFreeze(command.freeze)
		command.response <- err
		return
	case treeCommandProcess:
	default:
		if command.response != nil {
			command.response <- ErrEngineQuiescenceUnavailable
		}
		return
	}
	process := t.processes[command.processID]
	if process == nil {
		command.process.reply(processResponse{err: ErrProcessNotRunning})
		return
	}
	t.applyProcessCommand(process, command.process)
}

func (t *treeRuntime) applyProcessCommand(process *processState, command processCommand) {
	if t.fault != nil {
		command.reply(processResponse{err: process.handle.closedRequestError()})
		return
	}
	if command.kind == commandHostTerminated {
		if !process.status.Terminal() {
			process.recordHostTermination(command.hostErr)
			t.stopProcessTree(process)
		}
		return
	}
	if process.status.Terminal() {
		command.reply(processResponse{err: ErrProcessFinished})
		return
	}
	switch command.kind {
	case commandDeliverBatch:
		t.deliverSignals(process, command)
	case commandPause:
		command.reply(processResponse{err: process.requestPause(command.reason)})
	case commandResume:
		err := process.resume()
		if err == nil {
			t.publishEphemeralStatus(process)
			t.publishEventAfterCommit(process, EventProcessResumed, EventPhaseCommitted, 0, EffectID{}, emptyEventPayload())
		}
		command.reply(processResponse{err: err})
	case commandCancel:
		process.requestCancellation(command.cancellationIntent)
	case commandKill:
		command.reply(processResponse{err: process.requestKill(command.reason)})
	case commandResolveUnknownEffect:
		t.resolveUnknownEffect(process, command)
		return
	default:
		command.reply(processResponse{err: ErrInvalidProcessControl})
	}
	if process.pendingControl.hasTerminalIntent() {
		t.stopProcessTree(process)
	} else if process.pendingControl.pauseReason != "" {
		t.invalidateStep(process)
	}
	t.finishIfTerminal(process)
	if !process.status.Terminal() {
		t.enqueueProcess(process.handle.processID)
	}
}

func (t *treeRuntime) resolveUnknownEffect(process *processState, command processCommand) {
	if process.status.Terminal() || process.pendingControl.hasTerminalIntent() {
		command.reply(processResponse{err: ErrProcessFinished})
		return
	}
	if t.engine.durability == nil {
		command.reply(processResponse{err: process.resolveEffect(command.settlement)})
		t.enqueueProcess(process.handle.processID)
		return
	}
	if err := t.startUnknownResolutionCommit(process, command); err != nil {
		command.reply(processResponse{err: err})
		if !errors.Is(err, ErrEffectNotPending) {
			t.failDurability(err, process.handle.processID, command.settlement.EffectID())
		}
	}
}

func (t *treeRuntime) acquireFreeze(acquisition *treeFreezeAcquisition) {
	if acquisition == nil || acquisition.response == nil || acquisition.canceled == nil || t.freeze != nil {
		if acquisition != nil && acquisition.response != nil {
			acquisition.response <- treeFreezeAcquisitionResult{err: ErrEngineQuiescenceUnavailable}
		}
		return
	}
	if t.fault != nil {
		acquisition.response <- treeFreezeAcquisitionResult{err: t.fault}
		return
	}
	freeze := &treeFreeze{runtime: t}
	t.freeze = &activeTreeFreeze{acquisition: acquisition, freeze: freeze}
	t.freezeActive.Store(true)
	t.completeFreeze()
}

func (t *treeRuntime) completeFreeze() {
	if t.freeze == nil || t.freeze.ready || t.freezeBlockedByJob() {
		return
	}
	snapshot, err := t.captureTree()
	if err != nil {
		acquisition := t.freeze.acquisition
		t.releaseCurrentFreeze()
		acquisition.response <- treeFreezeAcquisitionResult{err: err}
		return
	}
	t.freeze.ready = true
	t.freeze.acquisition.response <- treeFreezeAcquisitionResult{
		freeze: t.freeze.freeze, snapshot: snapshot,
	}
}

func (t *treeRuntime) freezeBlockedByJob() bool {
	if t.freeze == nil {
		return false
	}
	allTerminal := true
	for _, process := range t.processes {
		allTerminal = allTerminal && process.status.Terminal()
	}
	if allTerminal {
		return len(t.jobs) != 0
	}
	for _, job := range t.jobs {
		if job.kind != processJobStep && job.kind != processJobRestore {
			return true
		}
	}
	return false
}

func (t *treeRuntime) captureTree() (TreeSnapshot, error) {
	wire := treeSnapshotWire{RootID: t.rootID}
	if t.incarnation.Valid() {
		incarnation := t.incarnation
		wire.IncarnationID = &incarnation
	}
	for _, process := range t.processes {
		snapshot, err := process.capture()
		if err != nil {
			return TreeSnapshot{}, err
		}
		wire.ProcessSnapshots = append(wire.ProcessSnapshots, snapshot)
	}
	for _, registration := range t.childWaits {
		wire.ChildWaits = append(wire.ChildWaits, childWaitSnapshotWire{
			ParentProcessID: registration.parent,
			WaitID:          registration.waitID,
			Spec:            childWaitSpecWireFromValue(registration.spec),
		})
	}
	return newTreeSnapshot(wire)
}

func (t *treeRuntime) releaseFreeze(freeze *treeFreeze) error {
	if t.freeze == nil || freeze == nil || t.freeze.freeze != freeze {
		return ErrEngineQuiescenceUnavailable
	}
	t.releaseCurrentFreeze()
	return nil
}

// releaseCurrentFreeze is used only by the tree owner after it has selected
// the active freeze. External capabilities still pass through releaseFreeze so
// stale or foreign authority is rejected rather than silently accepted.
func (t *treeRuntime) releaseCurrentFreeze() {
	t.freeze = nil
	t.freezeActive.Store(false)
	for _, process := range t.processes {
		if !process.status.Terminal() {
			t.enqueueProcess(process.handle.processID)
		}
	}
}

func (t *treeRuntime) invalidateStep(process *processState) {
	job := t.jobs[process.handle.processID]
	if job == nil || job.kind != processJobStep || job.stale {
		return
	}
	job.stale = true
	job.cancel()
}

// Stopping owned work cannot wait for an ancestor's external call to return.
// Terminal intermediate Processes still own descendants that may be draining.
func (t *treeRuntime) stopProcessTree(process *processState) {
	termination := process.effectiveTermination()
	if !process.status.Terminal() {
		if job := t.jobs[process.handle.processID]; job != nil {
			if job.kind == processJobStep || job.kind == processJobRestore {
				job.stale = true
			}
			if job.cancel != nil {
				job.cancel()
			}
		}
		t.enqueueProcess(process.handle.processID)
	}
	for _, child := range t.processes {
		parentID, isChild := child.handle.relation.ParentID()
		if !isChild || parentID != process.handle.processID {
			continue
		}
		if !child.status.Terminal() {
			child.recordParentTermination(termination)
		}
		t.stopProcessTree(child)
	}
}

func (t *treeRuntime) applyPendingControl(process *processState) bool {
	if process.pendingControl.hasTerminalIntent() {
		t.installTermination(process, stepOutcome{})
		return true
	}
	if !process.applyPendingPause() {
		return false
	}
	t.publishEphemeralStatus(process)
	t.publishEventAfterCommit(process, EventProcessPaused, EventPhaseCommitted, 0, EffectID{}, emptyEventPayload())
	return true
}

func (t *treeRuntime) deliverChildWaitSatisfied(process *processState, signal Signal) bool {
	accepted, err := process.admitSignals([]Signal{signal}, signalSourceChildWait)
	if err != nil {
		if errors.Is(err, ErrResourceLimitExceeded) {
			process.recordFailure(FailureKindExecution, "engine.limit.child_wait_signal", err)
		} else {
			process.recordFailure(FailureKindContract, "engine.child.wait.satisfaction.invalid", err)
		}
		return false
	}
	if accepted {
		t.publishEphemeralStatus(process)
		for _, event := range t.prepareSignalEvents(process, []Signal{signal}) {
			t.publishPreparedEventAfterCommit(process, event)
		}
	}
	return accepted || process.mailbox.contains(signal.ID())
}

func (t *treeRuntime) deliverSignals(process *processState, command processCommand) {
	if len(command.signalRequests) == 0 {
		command.reply(processResponse{err: ErrInvalidSignalRequest})
		return
	}
	signals := make([]Signal, 0, len(command.signalRequests))
	for _, request := range command.signalRequests {
		signal, err := request.signal()
		if err != nil {
			command.reply(processResponse{err: err})
			return
		}
		signals = append(signals, signal)
	}
	accepted, err := process.admitSignals(signals, signalSourceExternal)
	if err != nil || !accepted {
		command.reply(processResponse{err: err})
		return
	}
	events := t.prepareSignalEvents(process, signals)
	if t.engine.durability != nil {
		if err := t.startSignalCommit(process, command, events); err != nil {
			command.reply(processResponse{err: err})
			t.failDurability(err, process.handle.processID, EffectID{})
		}
		return
	}
	t.publishEphemeralStatus(process)
	for _, event := range events {
		t.publishPreparedEvent(process, event)
	}
	command.reply(processResponse{accepted: true})
}
