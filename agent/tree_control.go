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
	case treeCommandApplyFreeze:
		err := t.applyFreeze(command.freeze, command.projection)
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
	if command.kind == commandHostTerminated {
		if !process.status.Terminal() {
			process.recordHostTermination(command.hostErr)
			t.invalidateStep(process)
			t.markRunnable(process.controller.processID)
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
		t.markRunnable(process.controller.processID)
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
			t.failDurability(err, process.controller.processID, command.settlement.EffectID())
		}
	}
	return true
}

func (t *treeRuntime) acquireFreeze(acquisition *treeFreezeAcquisition) {
	if acquisition == nil || acquisition.response == nil || acquisition.canceled == nil ||
		(acquisition.mode != treeFreezeModeSnapshot && acquisition.mode != treeFreezeModeExclusive) ||
		t.freeze != nil {
		if acquisition != nil && acquisition.response != nil {
			acquisition.response <- treeFreezeAcquisitionResult{err: ErrEngineQuiescenceUnavailable}
		}
		return
	}
	freeze := &treeFreeze{runtime: t}
	t.freeze = &activeTreeFreeze{acquisition: acquisition, freeze: freeze}
	t.freezeHeld.Store(true)
	if acquisition.mode == treeFreezeModeExclusive {
		for _, process := range t.processes {
			t.invalidateStep(process)
		}
	}
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
	if t.freeze.acquisition.mode == treeFreezeModeExclusive {
		return len(t.jobs) != 0
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
	wire := treeSnapshotWire{Version: CurrentTreeSnapshotVersion, RootID: t.rootID}
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
	t.freezeHeld.Store(false)
	for _, process := range t.processes {
		if !process.status.Terminal() {
			t.markRunnable(process.controller.processID)
		}
	}
}

func (t *treeRuntime) applyFreeze(
	freeze *treeFreeze,
	projection *treeStateProjection,
) error {
	if t.freeze == nil || !t.freeze.ready || freeze == nil || t.freeze.freeze != freeze ||
		projection == nil {
		return ErrInvalidPreparedWaitingSubtreeCancellation
	}
	if t.engine.durability != nil && t.head.digest() != projection.sourceDigest {
		return ErrTreeIncarnationConflict
	}
	for _, change := range projection.changes {
		process := t.processes[change.processID]
		if err := change.validateSource(process); err != nil {
			return err
		}
	}
	for _, change := range projection.changes {
		process := t.processes[change.processID]
		change.apply(t.context, process)
	}
	t.childWaits = make(map[WaitID]*childWaitRegistration, len(projection.childWaits))
	for _, registration := range projection.childWaits {
		t.childWaits[registration.waitID] = registration
	}
	for _, change := range projection.changes {
		t.finishIfTerminal(t.processes[change.processID])
	}
	if t.engine.durability != nil {
		result, err := t.captureTree()
		if err != nil || result.Digest() != projection.resultingDigest {
			return ErrInvalidPreparedWaitingSubtreeCancellation
		}
		t.advanceHead(projection.resultingDigest)
		t.publishCheckpoint()
	}
	return t.releaseFreeze(freeze)
}

func (t *treeRuntime) invalidateStep(process *processState) {
	job := t.jobs[process.controller.processID]
	if job == nil || job.kind != processJobStep || job.stale {
		return
	}
	job.stale = true
	job.cancel()
}
