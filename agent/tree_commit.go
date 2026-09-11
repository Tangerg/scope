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

func (t *treeRuntime) effectRequestFor(
	process *processState,
	batchIndex uint32,
	record preparedEffect,
) EffectRequest {
	return newEffectRequest(
		process.handle.processID,
		t.incarnation,
		process.handle.deploymentRef,
		process.handle.relation,
		process.prepared.StepSequence,
		batchIndex,
		record.ID,
		record.Effect,
	)
}

func (t *treeRuntime) startPendingEffectCommit(
	process *processState,
	batchIndex uint32,
	record preparedEffect,
) error {
	request := t.effectRequestFor(process, batchIndex, record)
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
		kind: treeCommitEffectPending, processID: process.handle.processID,
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
	index, record, err := process.prepared.resolveUnknown(command.settlement)
	if err != nil {
		return err
	}
	var events []Event
	if event, ok := t.prepareSettlementEvent(process,
		record.ID, record.Effect.Target(), command.settlement.Status(),
		process.startedAt, nil,
	); ok {
		events = append(events, event)
	}
	snapshot, err := t.captureTree()
	if err != nil {
		return err
	}
	request := t.effectRequestFor(process, uint32(index), *record)
	boundary, err := newEffectBoundary(
		EffectBoundaryResolved, request, command.settlement, t.head.digest(), snapshot,
	)
	if err != nil {
		return err
	}
	commit := &treeCommit{
		kind: treeCommitEffectResolved, processID: process.handle.processID,
		effectID: record.ID, snapshot: snapshot,
		response: command.response, events: events,
	}
	t.startEffectCommit(commit, boundary)
	return nil
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
		kind: treeCommitSignals, processID: process.handle.processID,
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
	t.inFlightWork.Add(-1)
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
		t.discardChildStart(commit.child.plan)
	}
	t.failDurability(commitErr, commit.processID, unresolvedEffectID)
}

func (t *treeRuntime) applySuccessfulTreeCommit(commit *treeCommit) {
	if commit.snapshot.Valid() {
		t.advanceHead(commit.snapshot)
		t.publishAcknowledgedChanges()
	}
	process := t.processes[commit.processID]
	for _, event := range commit.events {
		t.publishPreparedEvent(process, event)
	}
	switch commit.kind {
	case treeCommitEffectPending, treeCommitEffectSettled:
		t.enqueueProcess(commit.processID)
	case treeCommitEffectResolved:
		if commit.response != nil {
			commit.response <- processResponse{}
		}
		t.enqueueProcess(commit.processID)
	case treeCommitChildOutcome:
		if err := t.publishChildOutcome(commit.child); err != nil {
			t.discardChildStart(commit.child.plan)
			t.failDurability(err, commit.processID, commit.effectID)
			return
		}
		t.enqueueProcess(commit.processID)
	case treeCommitCheckpoint:
	case treeCommitSignals:
		commit.response <- processResponse{accepted: true}
		t.enqueueProcess(commit.processID)
	}
}

// The runtime releases the reservation it currently owns: provisional before
// installation, committed after installation. Published children never use this path.
func (t *treeRuntime) discardChildStart(plan *childStartPlan) {
	if plan == nil {
		return
	}
	parentID, _ := plan.relation.ParentID()
	parent := t.processes[parentID]
	if child := t.processes[plan.childID]; child != nil {
		t.removeProcess(plan.childID)
		if parent != nil {
			parent.releaseCommittedChildBudget(plan.spec.Budget)
		}
	} else if parent != nil {
		parent.releaseProvisionalChildBudget(plan.spec.Budget)
	}
	delete(t.queued, plan.childID)
	delete(t.pendingPublications, plan.childID)
	for index, processID := range t.processQueue {
		if processID == plan.childID {
			t.processQueue = append(t.processQueue[:index], t.processQueue[index+1:]...)
			break
		}
	}
	t.engine.discardProcessStartReservation(plan.childID)
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
		t.engine.publishReservedProcess(child.handle)
	}
	parent := t.processes[pending.parentID]
	if pending.event.ProcessID().Valid() {
		t.publishPreparedEvent(parent, pending.event)
	} else {
		t.publishSettlementEvent(parent, pending.effectID, EffectTargetFramework,
			pending.childSettlementStatus(), pending.startedAt, nil,
		)
	}
	return nil
}

func (t *treeRuntime) tryStartCheckpoint() bool {
	if t.engine.durability == nil || t.fault != nil || t.commit != nil ||
		t.freeze != nil {
		return false
	}
	if len(t.jobs) != 0 || len(t.processQueue) != 0 {
		ready := false
		for processID := range t.pendingPublications {
			process := t.processes[processID]
			if process.status.Terminal() || process.status == StatusWaiting || process.status == StatusPaused {
				ready = true
				break
			}
		}
		if !ready {
			return false
		}
	}
	kind := t.checkpointKind()
	snapshot, err := t.captureTree()
	if err != nil {
		t.failDurability(err, ProcessID{}, EffectID{})
		return true
	}
	if snapshot.Digest() == t.head.digest() {
		// Control changes can return to the acknowledged state without changing
		// its recovery cut. Publishing those facts must not require another write.
		pending := len(t.pendingPublications) != 0
		t.publishAcknowledgedChanges()
		return pending
	}
	if err := t.startCheckpointCommit(kind, snapshot); err != nil {
		t.failDurability(err, ProcessID{}, EffectID{})
	}
	return true
}

func (t *treeRuntime) checkpointKind() TreeCheckpointKind {
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
		return TreeCheckpointProgress
	}
	if allTerminal {
		return TreeCheckpointTerminal
	}
	return TreeCheckpointParked
}

func (t *treeRuntime) stageTerminal(process *processState) {
	if process == nil || !process.status.Terminal() {
		return
	}
	processID := process.handle.processID
	publication := t.pendingPublications[processID]
	if publication.terminal {
		return
	}
	payload := terminalEventPayload(process)
	event, prepared := t.prepareEvent(process,
		EventProcessFinished, EventPhaseCommitted, 0, EffectID{}, payload,
	)
	t.propagateProcessTermination(process)
	if prepared {
		publication.events = append(publication.events, event)
	}
	publication.terminal = true
	t.pendingPublications[processID] = publication
}

func (t *treeRuntime) stageCommittedEvent(event Event) {
	if !event.Valid() || event.Phase() != EventPhaseCommitted || event.Relation().RootID() != t.rootID ||
		t.processes[event.ProcessID()] == nil {
		panic("agent: invalid committed Event")
	}
	processID := event.ProcessID()
	publication := t.pendingPublications[processID]
	publication.events = append(publication.events, event)
	t.pendingPublications[processID] = publication
}

func (t *treeRuntime) publishAcknowledgedChanges() {
	for _, process := range t.processesInCanonicalOrder() {
		processID := process.handle.processID
		publication, pending := t.pendingPublications[processID]
		if !pending {
			continue
		}
		for _, event := range publication.events {
			t.publishPreparedEvent(process, event)
		}
		if !publication.terminal {
			delete(t.pendingPublications, processID)
			continue
		}
		process.handle.publishResult(process.result())
		process.handle.finishBookkeeping()
		delete(t.pendingPublications, processID)
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
			t.abandonChildStartJob(job)
		}
	}
	clear(t.pendingPublications)
	clear(t.queued)
	t.processQueue = nil
	for _, process := range t.processesInCanonicalOrder() {
		select {
		case <-process.handle.outcomePublished:
			continue
		default:
		}
		processID := process.handle.processID
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
			t.removeProcess(processID)
			continue
		}
		failure := newTreeDurabilityFailure(cause)
		payload, _ := json.Marshal(runtimeStoppedEventPayload{
			FailureKind: failure.Kind(), FailureCode: failure.Code(),
		})
		t.publishEvent(process, EventRuntimeStopped, EventPhaseAttempt, 0, EffectID{}, payload)
		process.handle.publishRuntimeFailure(&RuntimeError{
			processID: processID, incarnationID: t.incarnation, headDigest: t.head.digest(),
			unresolvedEffectIDs: canonicalEffectIDs(unresolvedByProcess[processID]), cause: cause,
		}, acknowledged)
		process.handle.finishBookkeeping()
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
			left.handle.relation.Depth(),
			right.handle.relation.Depth(),
		); order != 0 {
			return order
		}
		return cmp.Compare(
			left.handle.processID.String(),
			right.handle.processID.String(),
		)
	})
	return processes
}

func (t *treeRuntime) abandonChildStartJob(job *processJob) {
	if job == nil || job.childStart == nil {
		return
	}
	plan := job.childStart
	job.childStart = nil
	t.discardChildStart(plan)
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
	t.inFlightWork.Add(1)
}
