package agent

import (
	"errors"
)

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
			t.invalidateStep(process)
			t.enqueueProcess(process.handle.processID)
		}
		return
	}
	if command.kind == commandResolveUnknownEffect && t.resolveUnknownEffect(process, command) {
		return
	}
	process.applyCommand(t.context, command)
	if process.pendingControl.hasTerminalIntent() || process.pendingControl.pauseReason != "" {
		t.invalidateStep(process)
	}
	t.finishIfTerminal(process)
	if !process.status.Terminal() {
		t.enqueueProcess(process.handle.processID)
	}
}

func (t *treeRuntime) resolveUnknownEffect(process *processState, command processCommand) bool {
	if process.pendingControl.hasTerminalIntent() {
		command.reply(processResponse{err: ErrProcessFinished})
		return true
	}
	if t.engine.durability == nil {
		return false
	}
	if err := t.startUnknownResolutionCommit(process, command); err != nil {
		command.reply(processResponse{err: err})
		if !errors.Is(err, ErrEffectNotPending) {
			t.failDurability(err, process.handle.processID, command.settlement.EffectID())
		}
	}
	return true
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
		if job.kind != processJobStep {
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
