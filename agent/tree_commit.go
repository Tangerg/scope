package agent

import (
	"cmp"
	"encoding/json"
	"errors"
	"slices"
)

const (
	treeDurabilityConflictCode  = "engine.tree.durability_conflict"
	treeDurabilityFailureCode   = "engine.tree.durability_failed"
	treeIncarnationConflictCode = "engine.tree.incarnation_conflict"
)

func effectRequestFor(
	process *processState,
	batchIndex uint32,
	record preparedEffectWire,
) EffectRequest {
	return newEffectRequest(
		process.controller.processID,
		process.runtime.incarnation,
		process.controller.deploymentRef,
		process.controller.relation,
		process.prepared.wire.StepSequence,
		batchIndex,
		record.ID,
		record.Effect,
	)
}

func (t *treeRuntime) startPendingEffectCommit(
	process *processState,
	batchIndex uint32,
	record preparedEffectWire,
) error {
	request := effectRequestFor(process, batchIndex, record)
	snapshot, err := t.captureTree()
	if err != nil {
		return err
	}
	boundary, err := newEffectBoundary(
		EffectBoundaryPending, request, Settlement{}, t.head.digest(), snapshot,
	)
	if err != nil {
		return err
	}
	commit := &treeCommit{
		kind: treeCommitEffectPending, processID: process.controller.processID,
		effectID: record.ID, snapshot: snapshot,
	}
	t.startEffectCommit(commit, boundary)
	return nil
}

func (t *treeRuntime) startEffectCommit(
	commit *treeCommit,
	boundary EffectBoundary,
) {
	if t.commit != nil || t.engine.durability == nil || !boundary.Valid() ||
		commit == nil || !commit.processID.Valid() {
		panic("agent: invalid concurrent tree commit")
	}
	t.setTreeCommit(commit)
	go func() {
		err := commitEffectBoundary(t.context, t.engine.durability, boundary)
		t.commitDone <- treeCommitCompletion{commit: commit, err: err}
	}()
}

func (t *treeRuntime) startUnknownResolutionCommit(
	process *processState,
	command processCommand,
) error {
	if process.prepared == nil || !command.settlement.Valid() ||
		command.settlement.Status() == SettlementStatusUnknown {
		return ErrEffectNotPending
	}
	for index := range process.prepared.wire.Effects {
		record := &process.prepared.wire.Effects[index]
		if record.ID != command.settlement.EffectID() {
			continue
		}
		if !record.unknown() {
			return ErrEffectNotPending
		}
		if err := record.resolveUnknown(command.settlement); err != nil {
			return ErrEffectNotPending
		}
		var events []Event
		if event, ok := process.prepareSettlementEvent(
			record.ID, record.Effect.Target(), command.settlement.Status(),
			process.startedAt,
		); ok {
			events = append(events, event)
		}
		snapshot, err := t.captureTree()
		if err != nil {
			return err
		}
		request := effectRequestFor(process, uint32(index), *record)
		boundary, err := newEffectBoundary(
			EffectBoundaryResolved, request, command.settlement, t.head.digest(), snapshot,
		)
		if err != nil {
			return err
		}
		commit := &treeCommit{
			kind: treeCommitEffectResolved, processID: process.controller.processID,
			effectID: record.ID, snapshot: snapshot,
			response: command.response, events: events,
		}
		t.startEffectCommit(commit, boundary)
		return nil
	}
	return ErrEffectNotPending
}

func (p *pendingChildOutcome) childSettlementStatus() SettlementStatus {
	if _, failed := p.result.result.Failure(); failed {
		return SettlementStatusFailed
	}
	return SettlementStatusSucceeded
}

func (t *treeRuntime) startCheckpointCommit(
	kind TreeCheckpointKind,
	snapshot TreeSnapshot,
) error {
	return t.startCheckpoint(&treeCommit{kind: treeCommitCheckpoint, snapshot: snapshot}, kind)
}

func (t *treeRuntime) startSignalCommit(process *processState, command processCommand, events []Event) error {
	snapshot, err := t.captureTree()
	if err != nil {
		return err
	}
	return t.startCheckpoint(&treeCommit{
		kind: treeCommitSignals, processID: process.controller.processID,
		snapshot: snapshot, response: command.response, events: events,
	}, TreeCheckpointInput)
}

func (t *treeRuntime) startCheckpoint(commit *treeCommit, kind TreeCheckpointKind) error {
	checkpoint, err := newTreeCheckpoint(kind, t.head.digest(), commit.snapshot)
	if err != nil {
		return err
	}
	if t.commit != nil || t.engine.durability == nil {
		return errors.New("invalid concurrent tree checkpoint")
	}
	t.setTreeCommit(commit)
	go func() {
		err := commitTreeCheckpoint(t.context, t.engine.durability, checkpoint)
		t.commitDone <- treeCommitCompletion{commit: commit, err: err}
	}()
	return nil
}

func (t *treeRuntime) applyTreeCommitCompletion(completion treeCommitCompletion) {
	commit := t.commit
	if commit == nil || completion.commit != commit {
		return
	}
	t.commit = nil
	t.inflight.Add(-1)
	if completion.err != nil {
		t.applyFailedTreeCommit(commit, completion.err)
		return
	}
	t.applySuccessfulTreeCommit(commit)
}

func (t *treeRuntime) applyFailedTreeCommit(commit *treeCommit, commitErr error) {
	if commit.response != nil {
		commit.response <- processResponse{err: commitErr}
	}
	unresolvedEffectID := commit.effectID
	if commit.kind == treeCommitEffectPending {
		unresolvedEffectID = EffectID{}
	}
	if commit.kind == treeCommitChildOutcome {
		t.discardProspectiveChild(commit.child)
	}
	t.failDurability(commitErr, commit.processID, unresolvedEffectID)
}

func (t *treeRuntime) applySuccessfulTreeCommit(commit *treeCommit) {
	if commit.snapshot.Valid() {
		t.advanceHead(commit.snapshot)
		t.publishCheckpoint()
	}
	process := t.processes[commit.processID]
	switch commit.kind {
	case treeCommitEffectPending:
		t.markRunnable(commit.processID)
	case treeCommitEffectSettled:
		for _, event := range commit.events {
			process.publishPreparedEvent(t.context, event)
		}
		t.markRunnable(commit.processID)
	case treeCommitEffectResolved:
		for _, event := range commit.events {
			process.publishPreparedEvent(t.context, event)
		}
		if commit.response != nil {
			commit.response <- processResponse{}
		}
		t.markRunnable(commit.processID)
	case treeCommitChildOutcome:
		if err := t.publishChildOutcome(commit.child); err != nil {
			t.failDurability(err, commit.processID, commit.effectID)
			return
		}
		t.markRunnable(commit.processID)
	case treeCommitCheckpoint:
	case treeCommitSignals:
		for _, event := range commit.events {
			process.publishPreparedEvent(t.context, event)
		}
		commit.response <- processResponse{accepted: true}
		t.markRunnable(commit.processID)
	}
}

func (t *treeRuntime) discardProspectiveChild(pending *pendingChildOutcome) {
	if pending == nil || pending.plan == nil ||
		!pending.result.started() {
		return
	}
	delete(t.processes, pending.plan.childID)
	delete(t.queued, pending.plan.childID)
	for index, processID := range t.runnable {
		if processID == pending.plan.childID {
			t.runnable = append(t.runnable[:index], t.runnable[index+1:]...)
			break
		}
	}
	pending.plan.engine.discardProcessStartReservation(pending.plan.childID)
	if parent := t.processes[pending.parentID]; parent != nil {
		parent.releaseCommittedChildBudget(pending.plan.spec.Budget)
	}
}

func (t *treeRuntime) publishChildOutcome(pending *pendingChildOutcome) error {
	if pending == nil || pending.plan == nil {
		return errors.New("child outcome is incomplete")
	}
	if pending.result.started() {
		child := t.processes[pending.plan.childID]
		if child == nil {
			return errors.New("started child is missing from prospective tree")
		}
		pending.plan.engine.publishReservedProcess(child.controller)
	}
	parent := t.processes[pending.parentID]
	if pending.event.ProcessID().Valid() {
		parent.publishPreparedEvent(t.context, pending.event)
	} else {
		parent.publishSettlementEvent(
			t.context, pending.effectID, EffectTargetFramework,
			pending.childSettlementStatus(), pending.startedAt,
		)
	}
	return nil
}

func (t *treeRuntime) tryStartCheckpoint() bool {
	if t.engine.durability == nil || t.fault != nil || t.commit != nil ||
		t.freeze != nil || len(t.jobs) != 0 || len(t.runnable) != 0 {
		return false
	}
	kind, safe := t.checkpointKind()
	if !safe {
		return false
	}
	snapshot, err := t.captureTree()
	if err != nil {
		t.failDurability(err, ProcessID{}, EffectID{})
		return true
	}
	if snapshot.Digest() == t.head.digest() {
		return false
	}
	if err := t.startCheckpointCommit(kind, snapshot); err != nil {
		t.failDurability(err, ProcessID{}, EffectID{})
	}
	return true
}

func (t *treeRuntime) checkpointKind() (TreeCheckpointKind, bool) {
	allTerminal := true
	for _, process := range t.processes {
		if process.status.Terminal() {
			continue
		}
		allTerminal = false
		if process.status == StatusWaiting || process.status == StatusPaused ||
			process.prepared != nil && process.prepared.hasUnknownSettlement() {
			continue
		}
		return TreeCheckpointInvalid, false
	}
	if allTerminal {
		return TreeCheckpointTerminal, true
	}
	return TreeCheckpointParked, true
}

func (t *treeRuntime) stageTerminal(process *processState) {
	if process == nil || !process.status.Terminal() {
		return
	}
	processID := process.controller.processID
	publication := t.checkpointPending[processID]
	if publication.terminal {
		return
	}
	payload := terminalEventPayload(process)
	event, prepared := process.prepareEvent(
		EventProcessFinished, EventPhaseCommitted, 0, EffectID{}, payload,
	)
	t.processFinished(process)
	if prepared {
		publication.events = append(publication.events, event)
	}
	publication.terminal = true
	t.checkpointPending[processID] = publication
}

func (t *treeRuntime) stageCheckpointEvent(event Event) {
	if !event.Valid() || event.Relation().RootID() != t.rootID ||
		t.processes[event.ProcessID()] == nil {
		panic("agent: invalid checkpoint Event")
	}
	processID := event.ProcessID()
	publication := t.checkpointPending[processID]
	publication.events = append(publication.events, event)
	t.checkpointPending[processID] = publication
}

func (t *treeRuntime) publishCheckpoint() {
	for _, process := range t.processesInCanonicalOrder() {
		processID := process.controller.processID
		publication, pending := t.checkpointPending[processID]
		if !pending {
			continue
		}
		for _, event := range publication.events {
			process.publishPreparedEvent(t.context, event)
		}
		if !publication.terminal {
			delete(t.checkpointPending, processID)
			continue
		}
		process.controller.complete(process.result())
		process.controller.markTreeSettled()
		delete(t.checkpointPending, processID)
	}
}

func terminalEventPayload(process *processState) json.RawMessage {
	usage := process.usage
	eventPayload := processFinishedEventPayload{
		ProcessStatus:    process.status,
		TerminationCause: process.termination.Cause(),
		Usage:            &usage,
	}
	if failure, failed := process.termination.Failure(); failed {
		eventPayload.FailureKind = failure.Kind()
		eventPayload.FailureCode = failure.Code()
	}
	payload, _ := json.Marshal(eventPayload)
	return payload
}

func (t *treeRuntime) failDurability(
	cause error,
	processID ProcessID,
	effectID EffectID,
) {
	if t.fault != nil {
		return
	}
	t.fault = cause
	t.head.finish(cause)
	unresolvedByProcess := make(map[ProcessID][]EffectID, len(t.processes))
	for candidateID, process := range t.processes {
		unresolvedByProcess[candidateID] = process.unknownEffectIDs()
	}
	if processID.Valid() && effectID.Valid() {
		unresolvedByProcess[processID] = append(
			unresolvedByProcess[processID], effectID,
		)
	}
	for candidateID, job := range t.jobs {
		job.stale = true
		if job.cancel != nil {
			job.cancel()
		}
		switch job.kind {
		case processJobDispatch, processJobChildStart:
			if job.effectID.Valid() {
				unresolvedByProcess[candidateID] = append(
					unresolvedByProcess[candidateID], job.effectID,
				)
			}
		}
		if job.kind == processJobChildStart {
			t.abandonChildStartJob(t.processes[candidateID], job)
		}
	}
	clear(t.checkpointPending)
	clear(t.queued)
	t.runnable = nil
	for _, process := range t.processesInCanonicalOrder() {
		select {
		case <-process.controller.done:
			continue
		default:
		}
		processID := process.controller.processID
		var acknowledged ProcessSnapshot
		for _, snapshot := range t.head.snapshot.ProcessSnapshots() {
			if snapshot.ProcessID() == processID {
				acknowledged = snapshot
				break
			}
		}
		if !acknowledged.Valid() {
			// A prospective child that never entered an acknowledged head has
			// no published lifecycle to stop.
			t.engine.discardProcessStartReservation(processID)
			delete(t.processes, processID)
			continue
		}
		failure := newTreeDurabilityFailure(cause)
		payload, _ := json.Marshal(runtimeStoppedEventPayload{
			FailureKind: failure.Kind(), FailureCode: failure.Code(),
		})
		process.publishEvent(t.context, EventRuntimeStopped, EventPhaseAttempt, 0, EffectID{}, payload)
		process.controller.stopRuntime(&RuntimeError{
			processID: processID, incarnationID: t.incarnation, headDigest: t.head.digest(),
			unresolvedEffectIDs: canonicalEffectIDs(unresolvedByProcess[processID]), cause: cause,
		}, acknowledged)
		process.controller.markTreeSettled()
	}
	if t.freeze != nil {
		acquisition := t.freeze.acquisition
		t.releaseCurrentFreeze()
		acquisition.response <- treeFreezeAcquisitionResult{err: cause}
	}
}

func (t *treeRuntime) processesInCanonicalOrder() []*processState {
	processes := make([]*processState, 0, len(t.processes))
	for _, process := range t.processes {
		processes = append(processes, process)
	}
	slices.SortFunc(processes, func(left, right *processState) int {
		if order := cmp.Compare(
			left.controller.relation.Depth(),
			right.controller.relation.Depth(),
		); order != 0 {
			return order
		}
		return cmp.Compare(
			left.controller.processID.String(),
			right.controller.processID.String(),
		)
	})
	return processes
}

func (t *treeRuntime) abandonChildStartJob(
	parent *processState,
	job *processJob,
) {
	if parent == nil || job == nil || job.childStart == nil {
		return
	}
	plan := job.childStart
	job.childStart = nil
	plan.engine.discardProcessStartReservation(plan.childID)
	parent.releaseProvisionalChildBudget(plan.spec.Budget)
}

func newTreeDurabilityFailure(cause error) Failure {
	kind := FailureKindExternal
	code := treeDurabilityFailureCode
	switch {
	case errors.Is(cause, ErrDurabilityConflict):
		kind = FailureKindContract
		code = treeDurabilityConflictCode
	case errors.Is(cause, ErrTreeIncarnationConflict):
		code = treeIncarnationConflictCode
	}
	return newEngineFailure(kind, code, cause)
}

func (t *treeRuntime) setTreeCommit(commit *treeCommit) {
	if commit == nil || t.commit != nil {
		panic("agent: invalid concurrent tree commit")
	}
	t.commit = commit
	t.inflight.Add(1)
}
