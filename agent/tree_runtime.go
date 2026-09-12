package agent

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"slices"
	"sync"
	"sync/atomic"
	"time"
)

const (
	childControlNotOwnedCode = "engine.child.control.not_owned"
	childSignalRejectedCode  = "engine.child.signal.rejected"
)

// treeRuntime serializes authoritative changes because sibling jobs must not
// publish incompatible tree cuts. Fenced completions let computation and dispatch
// run concurrently without sharing commit authority.
type treeRuntime struct {
	// Incarnation and head travel together to prevent a retired writer from
	// advancing the current tree.
	engine      *Engine
	rootID      ProcessID
	incarnation TreeIncarnationID
	head        *treeHead

	// External readers need scheduling liveness without acquiring execution
	// state. Atomics expose that view while commands and completions preserve
	// one mutation owner.
	inFlightWork atomic.Int64
	freezeActive atomic.Bool
	context      context.Context
	commands     chan treeCommand
	controls     chan treeCommand
	completions  chan treeJobCompletion
	inspections  chan chan treeInspectionResponse

	// Everything below is owner-line state. Keeping it lock-free makes commit,
	// scheduling, freeze, and checkpoint order a single explicit state machine.
	processes           map[ProcessID]*processState
	childrenByParent    map[ProcessID][]ProcessID
	childWaits          map[ProcessID]map[WaitID]*childWaitRegistration
	joinCandidates      map[ProcessID]*processState
	processQueue        []ProcessID
	queued              map[ProcessID]struct{}
	jobs                map[ProcessID]*processJob
	commit              *treeCommit
	commitDone          chan treeCommitCompletion
	fault               error
	pendingPublications map[ProcessID]pendingProcessPublication
	freeze              *activeTreeFreeze
	done                chan struct{}
	finalInspection     treeInspectionResponse
}

type treeCommandKind uint8

const (
	treeCommandInvalid treeCommandKind = iota
	treeCommandProcess
	treeCommandAcquireFreeze
	treeCommandReleaseFreeze
)

type treeCommand struct {
	kind        treeCommandKind
	processID   ProcessID
	process     processCommand
	freeze      *treeFreeze
	acquisition *treeFreezeAcquisition
	response    chan error
}

func newTreeProcessCommand(processID ProcessID, command processCommand) treeCommand {
	return treeCommand{kind: treeCommandProcess, processID: processID, process: command}
}

type treeFreezeAcquisition struct {
	response chan treeFreezeAcquisitionResult
	canceled chan struct{}
}

type treeFreezeAcquisitionResult struct {
	freeze   *treeFreeze
	snapshot TreeSnapshot
	err      error
}

type activeTreeFreeze struct {
	acquisition *treeFreezeAcquisition
	freeze      *treeFreeze
	ready       bool
}

type processAttempt uint64

type processJobKind uint8

const (
	processJobInvalid processJobKind = iota
	processJobStep
	processJobRestore
	processJobDispatch
	processJobChildStart
)

type processJob struct {
	kind       processJobKind
	attempt    processAttempt
	cancel     context.CancelFunc
	stale      bool
	effectID   EffectID
	childStart *childStartPlan
	startedAt  time.Time
}

type treeJobCompletion struct {
	processID  ProcessID
	attempt    processAttempt
	kind       processJobKind
	step       stepJobResult
	restore    restoreJobResult
	dispatch   dispatchJobResult
	childStart childStartJobResult
}

type treeCommitKind uint8

const (
	treeCommitInvalid treeCommitKind = iota
	treeCommitEffectPending
	treeCommitEffectSettled
	treeCommitEffectResolved
	treeCommitChildOutcome
	treeCommitCheckpoint
	treeCommitSignals
)

type treeCommit struct {
	kind      treeCommitKind
	processID ProcessID
	effectID  EffectID
	snapshot  TreeSnapshot
	response  chan processResponse
	events    []Event
	child     *pendingChildOutcome
}

type pendingChildOutcome struct {
	parentID  ProcessID
	effectID  EffectID
	plan      *childStartPlan
	result    childStartJobResult
	startedAt time.Time
	event     Event
}

func (p *pendingChildOutcome) childSettlementStatus() SettlementStatus {
	if _, failed := p.result.result.Failure(); failed {
		return SettlementStatusFailed
	}
	return SettlementStatusSucceeded
}

type treeCommitCompletion struct {
	commit *treeCommit
	err    error
}

type pendingProcessPublication struct {
	events   []Event
	terminal bool
}

type stepJobResult struct {
	transition       Transition
	deliveredSignals uint64
	candidate        Execution
	candidateState   ExecutionState
	stage            stepJobStage
	err              error
}

type restoreJobResult struct {
	execution Execution
	err       error
}

type stepJobStage uint8

const (
	stepJobStageInvalid stepJobStage = iota
	stepJobStageExecution
	stepJobStageSnapshot
	stepJobStageRestore
)

type dispatchJobResult struct {
	effectID   EffectID
	settlement Settlement
	dropped    uint64
	err        error
}

func newTreeRuntime(
	engine *Engine,
	rootID ProcessID,
	ctx context.Context,
	processes ...*processState,
) *treeRuntime {
	runtime := &treeRuntime{
		engine:              engine,
		rootID:              rootID,
		context:             context.WithoutCancel(requireContext(ctx)),
		commands:            make(chan treeCommand, treeCommandBufferCapacity),
		controls:            make(chan treeCommand, treeCommandBufferCapacity),
		completions:         make(chan treeJobCompletion),
		inspections:         make(chan chan treeInspectionResponse, treeCommandBufferCapacity),
		processes:           make(map[ProcessID]*processState, len(processes)),
		childrenByParent:    make(map[ProcessID][]ProcessID),
		childWaits:          make(map[ProcessID]map[WaitID]*childWaitRegistration),
		joinCandidates:      make(map[ProcessID]*processState),
		queued:              make(map[ProcessID]struct{}, len(processes)),
		jobs:                make(map[ProcessID]*processJob, len(processes)),
		commitDone:          make(chan treeCommitCompletion),
		pendingPublications: make(map[ProcessID]pendingProcessPublication),
		done:                make(chan struct{}),
	}
	for _, process := range processes {
		runtime.addProcess(process)
	}
	return runtime
}

func (t *treeRuntime) establishDurableHead(
	incarnation TreeIncarnationID,
	snapshot TreeSnapshot,
) {
	if t == nil || !incarnation.Valid() || !snapshot.Valid() ||
		snapshot.RootID() != t.rootID {
		panic("agent: invalid durable tree head")
	}
	snapshotIncarnation, durable := snapshot.IncarnationID()
	if !durable || snapshotIncarnation != incarnation {
		panic("agent: durable tree head incarnation mismatch")
	}
	t.incarnation = incarnation
	t.advanceHead(snapshot)
}

func (t *treeRuntime) run(rootContext context.Context) {
	defer t.finishRun()
	t.publishInitialProcessEvents()
	stopHostWatch := t.watchHostTermination(rootContext)
	defer stopHostWatch()

	for {
		advanced := t.advanceReadyWork()
		if t.canStop() {
			return
		}
		inspected := t.tryInspection()
		if !advanced && !inspected {
			t.waitForWork()
		}
	}
}

func (t *treeRuntime) finishRun() {
	inspection, err := t.buildInspection()
	inspection.Stopped = true
	t.finalInspection = treeInspectionResponse{inspection: inspection, err: err}
	close(t.done)
}

func (t *treeRuntime) publishInitialProcessEvents() {
	for _, process := range orderedProcesses(t.processes) {
		if process.status.Terminal() {
			continue
		}
		if process.restored {
			t.publishEvent(process, EventProcessRestored, EventPhaseCommitted, 0, EffectID{}, emptyEventPayload())
		} else {
			t.publishEvent(process, EventProcessStarted, EventPhaseCommitted, 0, EffectID{}, emptyEventPayload())
		}
	}
}

func (t *treeRuntime) watchHostTermination(rootContext context.Context) func() bool {
	return context.AfterFunc(rootContext, func() {
		select {
		case t.commands <- newTreeProcessCommand(
			t.rootID,
			processCommand{kind: commandHostTerminated, hostErr: rootContext.Err()},
		):
		case <-t.done:
		}
	})
}

func (t *treeRuntime) advanceReadyWork() bool {
	// Service every eligible lane once so a continuously ready lane cannot
	// prevent another from making progress. Each operation rechecks its barriers
	// because an earlier operation can acquire a freeze or start a commit.
	advanced := t.tryCommitCompletion()
	advanced = t.tryFreezeCancellation() || advanced
	advanced = t.tryControl() || advanced
	advanced = t.tryCommand() || advanced
	advanced = t.tryCompletion() || advanced
	advanced = t.advanceOne() || advanced
	advanced = t.publishJoins() || advanced
	return t.tryStartCheckpoint() || advanced
}

func (t *treeRuntime) waitForWork() {
	// Nil channels disable blocked lanes without duplicating event dispatch.
	// These are local selections; only the owner changes the actual barriers.
	commitDone := t.commitDone
	controls := t.controls
	commands := t.commands
	completions := t.completions
	freezeCanceled := t.freezeCanceled()
	if t.mutationsBlocked() {
		commands = nil
		completions = nil
	}
	if t.commit != nil {
		controls = nil
		freezeCanceled = nil
	} else {
		commitDone = nil
	}
	select {
	case completion := <-commitDone:
		t.applyTreeCommitCompletion(completion)
	case command := <-controls:
		t.applyCommand(command)
	case response := <-t.inspections:
		t.replyInspection(response)
	case command := <-commands:
		t.applyCommand(command)
	case completion := <-completions:
		t.applyCompletion(completion)
	case <-freezeCanceled:
		t.releaseCurrentFreeze()
	}
}

func (t *treeRuntime) tryCommitCompletion() bool {
	if t.commit == nil {
		return false
	}
	select {
	case completion := <-t.commitDone:
		t.applyTreeCommitCompletion(completion)
		return true
	default:
		return false
	}
}

func (t *treeRuntime) freezeCanceled() <-chan struct{} {
	if t.freeze == nil || t.freeze.acquisition == nil {
		return nil
	}
	return t.freeze.acquisition.canceled
}

func (t *treeRuntime) tryFreezeCancellation() bool {
	canceled := t.freezeCanceled()
	if canceled == nil {
		return false
	}
	select {
	case <-canceled:
		t.releaseCurrentFreeze()
		return true
	default:
		return false
	}
}

func (t *treeRuntime) tryControl() bool {
	if t.commit != nil {
		return false
	}
	select {
	case command := <-t.controls:
		t.applyCommand(command)
		return true
	default:
		return false
	}
}

func (t *treeRuntime) tryCommand() bool {
	if t.mutationsBlocked() {
		return false
	}
	select {
	case command := <-t.commands:
		t.applyCommand(command)
		return true
	default:
		return false
	}
}

func (t *treeRuntime) tryCompletion() bool {
	if t.mutationsBlocked() {
		return false
	}
	select {
	case completion := <-t.completions:
		t.applyCompletion(completion)
		return true
	default:
		return false
	}
}

func (t *treeRuntime) mutationsBlocked() bool {
	// Freeze acquisition stops new jobs, but active jobs may need a cancellation
	// command to drain. Only the completed snapshot barrier blocks both lanes.
	return t.commit != nil || t.freeze != nil && t.freeze.ready
}

func (t *treeRuntime) enqueueProcess(processID ProcessID) {
	process := t.processes[processID]
	if t.fault != nil || process == nil || process.status.Terminal() || t.jobs[processID] != nil {
		return
	}
	if _, exists := t.queued[processID]; exists {
		return
	}
	t.queued[processID] = struct{}{}
	t.processQueue = append(t.processQueue, processID)
}

func (t *treeRuntime) dequeueProcess() *processState {
	for len(t.processQueue) > 0 {
		processID := t.processQueue[0]
		t.processQueue = t.processQueue[1:]
		delete(t.queued, processID)
		process := t.processes[processID]
		if process != nil && !process.status.Terminal() && t.jobs[processID] == nil {
			return process
		}
	}
	return nil
}

func (t *treeRuntime) advanceOne() bool {
	if t.commit != nil || t.fault != nil || t.freeze != nil {
		return false
	}
	process := t.dequeueProcess()
	if process == nil {
		return false
	}
	if process.prepared != nil {
		t.advancePrepared(process)
		return true
	}
	if t.applyPendingControl(process) {
		t.finishIfTerminal(process)
		return true
	}
	if process.status == StatusRunning {
		t.startStep(process)
	}
	return true
}

func (t *treeRuntime) advancePrepared(process *processState) {
	index, record, err := process.prepared.nextEffect()
	if err != nil {
		t.failPreparedEffect(process, "engine.effect.phase.invalid", err)
		return
	}
	if process.pendingControl.hasTerminalIntent() {
		t.stopProcessTree(process)
		if record != nil {
			if record.Phase == effectPhasePending {
				if process.restoredPending.matches(record.ID) {
					t.recoverPendingEffect(process, uint32(index), record)
					return
				}
				// Only this incarnation can prove an unused dispatch permission.
				// A framework wait has no external work to collect or replay.
				record.revokeDispatch()
			}
		}
		t.terminatePrepared(process)
		t.finishIfTerminal(process)
		return
	}
	if record != nil {
		if record.unknown() {
			return
		}
		t.startPreparedEffect(process, index, record)
		return
	}
	if err := t.finalizePrepared(process); err != nil {
		if errors.Is(err, ErrResourceLimitExceeded) {
			process.recordFailure(FailureKindExecution, "engine.limit.child_wait_signal", err)
		} else {
			process.recordFailure(FailureKindContract, "engine.finalize.invalid", err)
		}
		t.terminatePrepared(process)
	}
	t.finishIfTerminal(process)
	if !process.status.Terminal() {
		t.enqueueProcess(process.handle.processID)
	}
}

func (t *treeRuntime) nextAttempt(process *processState) (processAttempt, bool) {
	if process.attemptSequence == math.MaxUint64 {
		t.failProcess(process,
			FailureKindContract,
			"engine.process.attempt_exhausted",
			errors.New("process attempt sequence is exhausted"),
		)
		t.finishIfTerminal(process)
		return 0, false
	}
	process.attemptSequence++
	return processAttempt(process.attemptSequence), true
}

func (t *treeRuntime) setProcessJob(processID ProcessID, job *processJob) {
	if !processID.Valid() || job == nil || t.jobs[processID] != nil {
		panic("agent: invalid concurrent Process job")
	}
	t.jobs[processID] = job
	t.inFlightWork.Add(1)
}

func (t *treeRuntime) canStop() bool {
	if t.freeze != nil || t.commit != nil || len(t.jobs) != 0 ||
		len(t.pendingPublications) != 0 {
		return false
	}
	for _, process := range t.processes {
		if !process.handle.joinDone() {
			return false
		}
	}
	return true
}

func (t *treeRuntime) prepareChildStart(
	process *processState,
	effectID EffectID,
	spec ChildSpec,
) childStartPreparation {
	if !spec.Valid() || !process.handle.relation.Valid() {
		return childStartPreparation{result: failedChildStart(
			spec, FailureKindContract, childRequestInvalidCode, ErrInvalidChildStart,
		)}
	}
	childID := effectID.childProcessID()
	relation := childProcessRelation(childID, process.handle.relation, spec.Key)
	requestDigest, err := spec.digest()
	if err != nil {
		return childStartPreparation{result: failedChildStart(
			spec, FailureKindContract, childRequestInvalidCode, err,
		)}
	}
	if existing, exists := t.engine.Process(childID); exists {
		if existing.Relation() == relation && existing.DeploymentRef() == spec.DeploymentRef &&
			existing.handle.childRequestDigest == requestDigest {
			return childStartPreparation{result: ChildStartResult{
				key: spec.Key, processID: childID, deploymentRef: spec.DeploymentRef,
			}}
		}
		return childStartPreparation{result: failedChildStart(
			spec, FailureKindContract, childIdentityConflictCode, ErrInvalidChildStart,
		)}
	}
	if !process.capabilities.Allows(spec.Capabilities) {
		return childStartPreparation{result: failedChildStart(
			spec, FailureKindContract, childCapabilityEscalationCode, ErrInvalidCapability,
		)}
	}
	if !process.reserveProvisionalChildBudget(spec.Budget) {
		return childStartPreparation{result: failedChildStart(
			spec, FailureKindExecution, childBudgetExhaustedCode, ErrResourceLimitExceeded,
		)}
	}
	transferred := false
	defer func() {
		if !transferred {
			process.releaseProvisionalChildBudget(spec.Budget)
		}
	}()
	childLimits, err := spec.Budget.limits(process.limits.MaxPendingSignals)
	if err != nil {
		return childStartPreparation{result: failedChildStart(
			spec, FailureKindExecution, childBudgetInvalidCode, err,
		)}
	}
	if reserveProcessStartErr := t.engine.reserveProcessStart(
		relation, spec.DeploymentRef, process.treeLimits, requestDigest,
	); reserveProcessStartErr != nil {
		if errors.Is(reserveProcessStartErr, ErrResourceLimitExceeded) {
			return childStartPreparation{result: failedChildStart(
				spec, FailureKindExecution, childTreeLimitCode, reserveProcessStartErr,
			)}
		}
		if errors.Is(reserveProcessStartErr, ErrEngineClosed) {
			return childStartPreparation{result: failedChildStart(
				spec, FailureKindExternal, childStartUnavailableCode, reserveProcessStartErr,
			)}
		}
		return childStartPreparation{result: failedChildStart(
			spec, FailureKindContract, childIdentityConflictCode, reserveProcessStartErr,
		)}
	}
	transferred = true
	return childStartPreparation{plan: &childStartPlan{
		admitter: t.engine.admitter, acknowledger: t.engine.initializationOutcomeAcknowledger,
		resolver: t.engine.resolver, parentDeployment: process.deployment,
		spec: spec, childID: childID, relation: relation,
		limits: childLimits, treeLimits: process.treeLimits,
		requestDigest: requestDigest,
	}}
}

// A tree-local control and its receipt change one authoritative cut. The target
// cannot consume the new input before that cut is acknowledged in durable mode.
func (t *treeRuntime) controlChild(parent *processState, index uint32, record *preparedEffect, startedAt time.Time) {
	request, err := decodeChildControlEffect(record.Effect.Payload())
	if err != nil {
		record.revokeDispatch()
		t.failPreparedEffect(parent, "engine.child.control.invalid", err)
		return
	}
	result := request.result()
	child := t.processes[request.ChildID]
	if child == nil || child.handle.relation.parentID != parent.handle.processID {
		result.failure = newEngineFailure(FailureKindContract, childControlNotOwnedCode,
			errors.New("control recipient is not a direct child"))
	} else {
		result = t.applyChildControl(child, request)
	}
	payload, err := result.MarshalJSON()
	if err == nil {
		status := SettlementStatusSucceeded
		if result.failure.Valid() {
			status = SettlementStatusFailed
		}
		var settlement Settlement
		settlement, err = NewSettlement(record.ID, status, payload)
		if err == nil {
			err = record.settle(settlement)
		}
	}
	if err != nil {
		t.failPreparedEffect(parent, "engine.child.control.settlement.invalid", err)
		return
	}
	t.publishSettlementEvent(parent, record.ID, EffectTargetFramework, record.Settlement.Status(), startedAt, nil)
	t.enqueueProcess(parent.handle.processID)
	if t.engine.durability == nil {
		return
	}
	snapshot, err := t.captureTree()
	if err == nil {
		var boundary EffectBoundary
		boundary, err = newEffectBoundary(EffectBoundarySettled, t.effectRequestFor(parent, index, *record),
			*record.Settlement, t.head.digest(), snapshot)
		if err == nil {
			t.startEffectCommit(&treeCommit{
				kind: treeCommitEffectSettled, processID: parent.handle.processID,
				effectID: record.ID, snapshot: snapshot,
			}, boundary)
		}
	}
	if err != nil {
		t.failDurability(err, parent.handle.processID, record.ID)
	}
}

func (t *treeRuntime) applyChildControl(child *processState, request childControlEffectWire) ChildControlResult {
	result := request.result()
	if request.Operation == frameworkEffectCancelChild {
		if !child.status.Terminal() {
			// The request decoder has already validated this exact reason.
			child.requestCancellation(cancellationIntent{owner: cancellationOwnerParent, reason: request.Reason})
			t.stopProcessTree(child)
		}
		return result
	}
	signal, err := request.Signal.signal()
	if err != nil {
		result.failure = newEngineFailure(FailureKindContract, childSignalRejectedCode, err)
		return result
	}
	accepted, err := child.admitSignals([]Signal{signal}, signalSourceExternal)
	if err != nil {
		result.failure = newEngineFailure(FailureKindExecution, childSignalRejectedCode, err)
		return result
	}
	if accepted {
		t.publishEphemeralStatus(child)
		for _, event := range t.prepareSignalEvents(child, []Signal{signal}) {
			t.publishPreparedEventAfterCommit(child, event)
		}
		t.enqueueProcess(child.handle.processID)
	}
	return result
}

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
	if t.commit != nil || t.engine.durability == nil || !boundary.kind.Valid() ||
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
	payload := process.terminalEventPayload()
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
	for _, process := range orderedProcesses(t.processes) {
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
		t.completeProcessBookkeeping(process)
		delete(t.pendingPublications, processID)
	}
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
	acknowledgedByID := make(map[ProcessID]ProcessSnapshot, len(t.head.snapshot.state.ProcessSnapshots))
	for _, snapshot := range t.head.snapshot.state.ProcessSnapshots {
		acknowledgedByID[snapshot.ProcessID()] = snapshot
	}
	for _, process := range orderedProcesses(t.processes) {
		select {
		case <-process.handle.outcomePublished:
			continue
		default:
		}
		processID := process.handle.processID
		acknowledged := acknowledgedByID[processID]
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
		t.completeProcessBookkeeping(process)
	}
	if t.freeze != nil {
		acquisition := t.freeze.acquisition
		t.releaseCurrentFreeze()
		acquisition.response <- treeFreezeAcquisitionResult{err: cause}
	}
}

func (t *treeRuntime) abandonChildStartJob(job *processJob) {
	if job == nil || job.childStart == nil {
		return
	}
	plan := job.childStart
	job.childStart = nil
	t.discardChildStart(plan)
}

func (t *treeRuntime) setTreeCommit(commit *treeCommit) {
	if commit == nil || t.commit != nil {
		panic("agent: invalid concurrent tree commit")
	}
	t.commit = commit
	t.inFlightWork.Add(1)
}

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
	for parentID, registrations := range t.childWaits {
		for _, registration := range registrations {
			wire.ChildWaits = append(wire.ChildWaits, childWaitSnapshotWire{
				ParentProcessID: parentID,
				WaitID:          registration.waitID,
				Spec:            registration.spec.wire(),
			})
		}
	}
	return treeSnapshotFromWire(wire)
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
	for _, childID := range t.childrenByParent[process.handle.processID] {
		child := t.processes[childID]
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

func (t *treeRuntime) publishEvent(
	process *processState,
	name string,
	phase EventPhase,
	step uint64,
	effectID EffectID,
	payload json.RawMessage,
) {
	event, ok := t.prepareEvent(process, name, phase, step, effectID, payload)
	if !ok {
		return
	}
	t.publishPreparedEvent(process, event)
}

func (t *treeRuntime) publishEventAfterCommit(
	process *processState,
	name string,
	phase EventPhase,
	step uint64,
	effectID EffectID,
	payload json.RawMessage,
) {
	event, ok := t.prepareEvent(process, name, phase, step, effectID, payload)
	if !ok {
		return
	}
	t.publishPreparedEventAfterCommit(process, event)
}

func (t *treeRuntime) publishPreparedEventAfterCommit(process *processState, event Event) {
	if t.engine.durability == nil {
		t.publishPreparedEvent(process, event)
		return
	}
	t.stageCommittedEvent(event)
}

func (t *treeRuntime) prepareEvent(
	process *processState,
	name string,
	phase EventPhase,
	step uint64,
	effectID EffectID,
	payload json.RawMessage,
) (Event, bool) {
	if process.processEventSequence == math.MaxUint64 {
		return Event{}, false
	}
	nextSequence := process.processEventSequence + 1
	event, err := newEvent(eventSpec{
		processSequence: nextSequence,
		processID:       process.handle.processID,
		deploymentRef:   process.deployment.DeploymentRef(),
		relation:        process.handle.relation,
		incarnationID:   t.incarnation,
		stepSequence:    step,
		effectID:        effectID,
		name:            name,
		phase:           phase,
		occurredAt:      time.Now(),
		payload:         payload,
	})
	if err != nil {
		return Event{}, false
	}
	return event, true
}

func (t *treeRuntime) publishPreparedEvent(process *processState, event Event) {
	if process.processEventSequence == math.MaxUint64 {
		return
	}
	// Prepared facts may wait for a durable acknowledgment while newer attempts
	// are observed. Only publication establishes their listener-visible order.
	process.processEventSequence++
	event.processSequence = process.processEventSequence
	t.engine.observation.publishEvent(context.WithoutCancel(t.context), event)
}

func (t *treeRuntime) publishEffectStarted(
	process *processState,
	step uint64,
	effectID EffectID,
	target EffectTarget,
) time.Time {
	payload, _ := json.Marshal(effectStartedEventPayload{EffectTarget: target})
	t.publishEvent(process, EventEffectStarted, EventPhaseAttempt, step, effectID, payload)
	return time.Now()
}

func (t *treeRuntime) publishSettlementEvent(
	process *processState,
	effectID EffectID,
	target EffectTarget,
	status SettlementStatus,
	startedAt time.Time,
	cause error,
) {
	event, ok := t.prepareSettlementEvent(process, effectID, target, status, startedAt, cause)
	if !ok {
		return
	}
	t.publishPreparedEvent(process, event)
}

func (t *treeRuntime) prepareSettlementEvent(
	process *processState,
	effectID EffectID,
	target EffectTarget,
	status SettlementStatus,
	startedAt time.Time,
	cause error,
) (Event, bool) {
	durationMS := time.Since(startedAt).Milliseconds()
	failureKind, failureCode := dispatchFailure(cause)
	payload, err := json.Marshal(effectFinishedEventPayload{
		EffectTarget: target, SettlementStatus: status,
		DurationMS: &durationMS, FailureKind: failureKind, FailureCode: failureCode,
	})
	if err != nil {
		return Event{}, false
	}
	return t.prepareEvent(process,
		EventEffectFinished, EventPhaseAttempt,
		process.prepared.StepSequence, effectID, payload,
	)
}

func (t *treeRuntime) prepareSignalEvents(process *processState, signals []Signal) []Event {
	var events []Event
	for _, signal := range signals {
		waitID, _ := signal.WaitID()
		payload, _ := json.Marshal(signalAcceptedEventPayload{SignalID: signal.ID().String(), WaitID: waitID.String()})
		if event, ok := t.prepareEvent(process, EventSignalAccepted, EventPhaseCommitted, 0, EffectID{}, payload); ok {
			events = append(events, event)
		}
	}
	return events
}

func (t *treeRuntime) publishEphemeralStatus(process *processState) {
	if t.engine.durability == nil {
		process.handle.updateStatus(process.status)
	}
}

func (t *treeRuntime) advanceHead(snapshot TreeSnapshot) {
	if t.head.digest() == snapshot.Digest() {
		return
	}
	previous := t.head
	t.head = &treeHead{snapshot: snapshot, advanced: make(chan struct{})}
	for _, snapshot := range snapshot.ProcessSnapshots() {
		if process := t.processes[snapshot.ProcessID()]; process != nil {
			process.handle.updateStatus(snapshot.Status())
		}
	}
	previous.finish(nil)
}

func (t *treeRuntime) inspect(ctx context.Context) (TreeInspection, error) {
	select {
	case <-t.done:
		return t.finalInspection.inspection.clone(), t.finalInspection.err
	default:
	}
	response := make(chan treeInspectionResponse, 1)
	select {
	case t.inspections <- response:
	case <-t.done:
		return t.finalInspection.inspection.clone(), t.finalInspection.err
	case <-ctx.Done():
		return TreeInspection{}, ctx.Err()
	}
	select {
	case result := <-response:
		return result.inspection, result.err
	case <-t.done:
		return t.finalInspection.inspection.clone(), t.finalInspection.err
	case <-ctx.Done():
		return TreeInspection{}, ctx.Err()
	}
}

func (t *treeRuntime) tryInspection() bool {
	select {
	case response := <-t.inspections:
		t.replyInspection(response)
		return true
	default:
		return false
	}
}

func (t *treeRuntime) replyInspection(response chan treeInspectionResponse) {
	inspection, err := t.buildInspection()
	response <- treeInspectionResponse{inspection: inspection, err: err}
}

func (t *treeRuntime) buildInspection() (TreeInspection, error) {
	inspection := TreeInspection{
		RootID: t.rootID, IncarnationID: t.incarnation, HeadDigest: t.head.digest(),
		CommitPending: t.commit != nil, Freeze: TreeFreezeNone,
	}
	if t.freeze != nil {
		inspection.Freeze = TreeFreezeAcquiring
		if t.freeze.ready {
			inspection.Freeze = TreeFreezeHeld
		}
	}
	var snapshots []ProcessSnapshot
	if t.head != nil {
		snapshots = t.head.snapshot.ProcessSnapshots()
	} else {
		for _, process := range orderedProcesses(t.processes) {
			snapshot, err := process.capture()
			if err != nil {
				return TreeInspection{}, err
			}
			snapshots = append(snapshots, snapshot)
		}
	}
	for _, snapshot := range snapshots {
		processID := snapshot.ProcessID()
		process := t.processes[processID]
		if process == nil {
			continue
		}
		report := ProcessInspection{Snapshot: snapshot, Work: ProcessWorkIdle}
		_, runtimeErr := process.handle.outcome()
		report.RuntimeError, _ = errors.AsType[*RuntimeError](runtimeErr)
		if job := t.jobs[processID]; job != nil {
			report.Stale = job.stale
			report.EffectID = job.effectID
			switch job.kind {
			case processJobStep:
				report.Work = ProcessWorkStep
			case processJobRestore:
				report.Work = ProcessWorkRestore
			case processJobDispatch:
				report.Work = ProcessWorkDispatch
			case processJobChildStart:
				report.Work = ProcessWorkChildStart
			}
		} else if _, queued := t.queued[processID]; queued && !process.status.Terminal() {
			report.Work = ProcessWorkQueued
		}
		inspection.Processes = append(inspection.Processes, report)
	}
	return inspection, nil
}

func (t *treeRuntime) startStep(process *processState) {
	if failure := process.stepSchedulingFailure(); failure != nil {
		t.failProcess(process, failure.kind, failure.code, failure.cause)
		t.finishIfTerminal(process)
		return
	}
	attempt, ok := t.nextAttempt(process)
	if !ok {
		return
	}
	sequence := process.committedSteps + 1
	t.publishEvent(process, EventStepStarted, EventPhaseAttempt, sequence, EffectID{}, emptyEventPayload())
	execution := process.execution
	process.execution = nil
	signals := process.mailbox.pending()
	// Host context values must not become unrecorded inputs to a pure Step.
	stepCtx, cancel := context.WithCancel(context.Background())
	t.setProcessJob(process.handle.processID, &processJob{
		kind: processJobStep, attempt: attempt, cancel: cancel, startedAt: time.Now(),
	})
	go func() {
		transition, err := stepExecution(stepCtx, execution, signals)
		result := stepJobResult{
			transition: transition, deliveredSignals: uint64(len(signals)),
			stage: stepJobStageExecution, err: err,
		}
		if err == nil {
			result.stage = stepJobStageSnapshot
			result.candidateState, result.err = captureExecution(execution)
		}
		if result.err == nil {
			result.stage = stepJobStageRestore
			result.candidate, result.err = restoreExecution(
				process.deployment.Definition(), result.candidateState,
			)
		}
		if result.err == nil {
			result.stage = stepJobStageInvalid
		}
		t.completions <- treeJobCompletion{
			processID: process.handle.processID,
			attempt:   attempt,
			kind:      processJobStep,
			step:      result,
		}
	}()
}

func (t *treeRuntime) startRestore(process *processState) {
	attempt, ok := t.nextAttempt(process)
	if !ok {
		return
	}
	processID := process.handle.processID
	definition := process.deployment.Definition()
	state := process.committedExecutionState
	t.setProcessJob(processID, &processJob{
		kind: processJobRestore, attempt: attempt, startedAt: time.Now(),
	})
	go func() {
		execution, err := restoreExecution(definition, state)
		t.completions <- treeJobCompletion{
			processID: processID, attempt: attempt, kind: processJobRestore,
			restore: restoreJobResult{execution: execution, err: err},
		}
	}()
}

func (t *treeRuntime) startPreparedEffect(process *processState, index int, record *preparedEffect) {
	if record.Phase == effectPhasePlanned {
		if err := record.begin(); err != nil {
			t.failPreparedEffect(process, "engine.effect.phase.invalid", err)
			return
		}
		if record.Effect.Target() == EffectTargetDispatcher && t.engine.durability != nil {
			if err := t.startPendingEffectCommit(process, uint32(index), *record); err != nil {
				t.failDurability(err, process.handle.processID, EffectID{})
			}
			return
		}
	}
	if record.Phase == effectPhasePending &&
		record.Effect.Target() == EffectTargetDispatcher &&
		process.restoredPending.matches(record.ID) {
		t.recoverPendingEffect(process, uint32(index), record)
		return
	}
	if record.Effect.Target() == EffectTargetFramework {
		process.restoredPending = restoredPendingEffect{}
		startedAt := t.publishEffectStarted(process, process.prepared.StepSequence, record.ID, EffectTargetFramework)
		operation, err := decodeFrameworkEffectOperation(record.Effect.Payload())
		if err == nil && operation == frameworkEffectStartChild {
			t.startChild(process, record, startedAt)
			return
		}
		if err == nil && (operation == frameworkEffectSignalChild || operation == frameworkEffectCancelChild) {
			t.controlChild(process, uint32(index), record, startedAt)
			return
		}
		if err := record.settleFramework(); err != nil {
			// Local wait preparation performed no external work.
			record.revokeDispatch()
			t.failPreparedEffect(process, "engine.framework_effect.settlement.invalid", err)
			return
		}
		t.publishSettlementEvent(process, record.ID, EffectTargetFramework, record.Settlement.Status(), startedAt, nil)
		t.enqueueProcess(process.handle.processID)
		return
	}
	t.startDispatch(process, uint32(index), *record)
}

func (t *treeRuntime) recoverPendingEffect(
	process *processState,
	batchIndex uint32,
	record *preparedEffect,
) {
	decision := process.restoredPending
	process.restoredPending = restoredPendingEffect{}
	if record.Effect.Target() == EffectTargetFramework {
		// The authoritative cut contains no published child. Admission may have
		// run, so retain a failed start instead of claiming it never began.
		spec, err := decodeChildStartEffect(record.Effect.Payload())
		if err == nil {
			result := failedChildStart(spec, FailureKindExecution, childStartInterruptedCode,
				errors.New("child publication interrupted by parent termination"))
			err = record.settleChildStart(result)
		}
		if err != nil {
			t.failPreparedEffect(process, childSettlementInvalidCode, err)
			return
		}
		t.enqueueProcess(process.handle.processID)
		return
	}
	if process.pendingControl.hasTerminalIntent() {
		decision.replayPolicy = ReplayPolicyNever
	}
	switch decision.replayPolicy {
	case ReplayPolicySameIdentity:
		t.startDispatch(process, batchIndex, *record)
	case ReplayPolicyNever:
		if err := record.settleUnknown(); err != nil {
			t.failPreparedEffect(process, "engine.effect.recovery.invalid", err)
			return
		}
		if t.engine.durability == nil {
			t.enqueueProcess(process.handle.processID)
			return
		}
		settlement := *record.Settlement
		snapshot, err := t.captureTree()
		if err != nil {
			t.failDurability(err, process.handle.processID, record.ID)
			return
		}
		boundary, err := newEffectBoundary(
			EffectBoundarySettled,
			t.effectRequestFor(process, batchIndex, *record),
			settlement,
			t.head.digest(),
			snapshot,
		)
		if err != nil {
			t.failDurability(err, process.handle.processID, record.ID)
			return
		}
		t.startEffectCommit(&treeCommit{
			kind: treeCommitEffectSettled, processID: process.handle.processID,
			effectID: record.ID, snapshot: snapshot,
		}, boundary)
	default:
		t.failPreparedEffect(
			process, "engine.effect.recovery.invalid", errInvalidReplayPolicy,
		)
	}
}

func (t *treeRuntime) startChild(
	process *processState,
	record *preparedEffect,
	startedAt time.Time,
) {
	spec, err := decodeChildStartEffect(record.Effect.Payload())
	if err != nil {
		record.revokeDispatch()
		t.failPreparedEffect(process, "engine.framework_effect.settlement.invalid", err)
		return
	}
	attempt, ok := t.nextAttempt(process)
	if !ok {
		return
	}
	preparation := t.prepareChildStart(process, record.ID, spec)
	if preparation.plan == nil {
		if err := t.settleChildStart(process, record.ID, preparation.result, startedAt); err != nil {
			t.failPreparedEffect(process, childSettlementInvalidCode, err)
			return
		}
		t.enqueueProcess(process.handle.processID)
		return
	}
	startCtx, cancel := context.WithCancel(t.context)
	job := &processJob{
		kind: processJobChildStart, attempt: attempt, effectID: record.ID,
		childStart: preparation.plan, startedAt: startedAt, cancel: cancel,
	}
	t.setProcessJob(process.handle.processID, job)
	go func() {
		result := preparation.plan.execute(startCtx)
		t.completions <- treeJobCompletion{
			processID:  process.handle.processID,
			attempt:    attempt,
			kind:       processJobChildStart,
			childStart: result,
		}
	}()
}

func (t *treeRuntime) startDispatch(
	process *processState,
	batchIndex uint32,
	record preparedEffect,
) {
	attempt, ok := t.nextAttempt(process)
	if !ok {
		return
	}
	request := t.effectRequestFor(process, batchIndex, record)
	startedAt := t.publishEffectStarted(process, process.prepared.StepSequence, record.ID, EffectTargetDispatcher)
	dispatchCtx, cancel := context.WithCancel(t.context)
	job := &processJob{
		kind:      processJobDispatch,
		attempt:   attempt,
		cancel:    cancel,
		effectID:  record.ID,
		startedAt: startedAt,
	}
	t.setProcessJob(process.handle.processID, job)
	var deltaMu sync.Mutex
	var deltaSequence, dropped uint64
	acceptingDeltas := true
	var emit DeltaEmitter
	if len(t.engine.observation.deltas) > 0 {
		emit = func(payload json.RawMessage) {
			deltaMu.Lock()
			defer deltaMu.Unlock()
			if !acceptingDeltas {
				return
			}
			deltaSequence++
			delta, err := newDelta(
				process.handle.processID, record.ID, t.incarnation,
				deltaSequence, time.Now(), payload,
			)
			if err != nil || !t.engine.observation.offerDelta(t.context, delta) {
				dropped++
			}
		}
	}

	go func() {
		settlement, err := dispatchEffect(
			dispatchCtx,
			process.deployment.effectDispatcher(),
			request,
			emit,
		)
		deltaMu.Lock()
		acceptingDeltas = false
		droppedCount := dropped
		deltaMu.Unlock()
		if err == nil && (!settlement.Valid() || settlement.EffectID() != record.ID) {
			err = ErrInvalidSettlement
		}
		if err != nil {
			pending := record
			if settleErr := pending.settleUnknown(); settleErr == nil {
				settlement = *pending.Settlement
			} else {
				settlement = Settlement{}
				err = errors.Join(err, settleErr)
			}
		}
		t.completions <- treeJobCompletion{
			processID: process.handle.processID,
			attempt:   attempt,
			kind:      processJobDispatch,
			dispatch: dispatchJobResult{
				err:        err,
				effectID:   record.ID,
				settlement: settlement,
				dropped:    droppedCount,
			},
		}
	}()
}

func (t *treeRuntime) applyCompletion(completion treeJobCompletion) {
	process := t.processes[completion.processID]
	job := t.jobs[completion.processID]
	if process == nil || job == nil || job.kind != completion.kind || job.attempt != completion.attempt {
		return
	}
	delete(t.jobs, completion.processID)
	t.inFlightWork.Add(-1)
	t.queueJoin(process)
	if job.cancel != nil {
		job.cancel()
	}
	if t.fault != nil {
		return
	}
	if job.stale {
		// Resumption requires the committed state to be executable. Terminal
		// intent needs only its saved evidence, so reconstruction is unnecessary.
		if completion.kind == processJobStep && !process.pendingControl.hasTerminalIntent() {
			t.startRestore(process)
		}
		if t.freeze == nil {
			t.enqueueProcess(completion.processID)
		}
		t.completeFreeze()
		return
	}
	switch completion.kind {
	case processJobStep:
		t.applyStepCompletion(process, job, completion.step)
	case processJobRestore:
		if completion.restore.err != nil {
			t.failProcess(process, failureKindForError(completion.restore.err), "execution.snapshot.unrestorable", completion.restore.err)
		} else {
			process.execution = completion.restore.execution
		}
	case processJobDispatch:
		t.applyDispatchCompletion(process, job, completion.dispatch)
	case processJobChildStart:
		t.applyChildStartCompletion(process, job, completion.childStart)
	}
	if t.commit != nil {
		t.completeFreeze()
		return
	}
	t.finishIfTerminal(process)
	if !process.status.Terminal() {
		t.enqueueProcess(completion.processID)
	}
	t.completeFreeze()
}

func (t *treeRuntime) applyChildStartCompletion(
	parent *processState,
	job *processJob,
	result childStartJobResult,
) {
	plan := job.childStart
	if plan == nil {
		if err := parent.prepared.settleUnknown(job.effectID); err != nil {
			t.failPreparedEffect(parent, childSettlementInvalidCode, err)
		}
		return
	}
	pending := &pendingChildOutcome{
		parentID: parent.handle.processID, effectID: job.effectID,
		plan: plan, result: result, startedAt: job.startedAt,
	}
	transferred := false
	var outcomeErr, checkpointErr error
	defer func() {
		if !transferred {
			t.discardChildStart(plan)
		}
		if outcomeErr != nil {
			t.failPreparedEffect(parent, childSettlementInvalidCode, outcomeErr)
		} else if checkpointErr != nil {
			t.failDurability(checkpointErr, parent.handle.processID, job.effectID)
		}
	}()
	if outcomeErr = t.applyChildOutcome(pending); outcomeErr != nil {
		return
	}
	if t.engine.durability != nil {
		if event, ok := t.prepareSettlementEvent(parent,
			job.effectID, EffectTargetFramework,
			pending.childSettlementStatus(), job.startedAt, nil,
		); ok {
			pending.event = event
		}
		snapshot, err := t.captureTree()
		if err == nil {
			err = t.startCheckpoint(&treeCommit{
				kind: treeCommitChildOutcome, processID: pending.parentID,
				effectID: pending.effectID, snapshot: snapshot, child: pending,
			}, TreeCheckpointChild)
		}
		checkpointErr = err
		transferred = err == nil
		return
	}
	if outcomeErr = t.publishChildOutcome(pending); outcomeErr != nil {
		return
	}
	transferred = true
}

func (t *treeRuntime) applyChildOutcome(pending *pendingChildOutcome) error {
	if pending == nil || pending.plan == nil {
		return errors.New("child outcome is incomplete")
	}
	parent := t.processes[pending.parentID]
	if parent == nil {
		return errors.New("child outcome parent is missing")
	}
	if pending.result.started() {
		if err := parent.commitProvisionalChildBudget(pending.plan.spec.Budget); err != nil {
			return err
		}
		handle := newProcessHandleState(
			pending.plan.relation,
			pending.result.deployment.DeploymentRef(),
			pending.plan.spec.Budget,
			pending.plan.spec.Capabilities,
			pending.plan.treeLimits,
			pending.result.startedAt,
			StatusRunning,
		)
		handle.childRequestDigest = pending.plan.requestDigest
		child := newProcessState(
			handle, pending.result.deployment, pending.result.execution,
			pending.result.state, pending.result.startedAt, pending.plan.limits,
		)
		t.addProcess(child)
		if parent.pendingControl.hasTerminalIntent() || parent.status.Terminal() {
			child.recordParentTermination(parent.effectiveTermination())
			t.stopProcessTree(child)
		}
	} else {
		t.discardChildStart(pending.plan)
	}
	_, err := t.applyChildStartSettlement(parent, pending.effectID, pending.result.result)
	return err
}

func (t *treeRuntime) settleChildStart(
	parent *processState,
	effectID EffectID,
	result ChildStartResult,
	startedAt time.Time,
) error {
	status, err := t.applyChildStartSettlement(parent, effectID, result)
	if err != nil {
		return err
	}
	t.publishSettlementEvent(parent, effectID, EffectTargetFramework, status, startedAt, nil)
	return nil
}

func (t *treeRuntime) applyChildStartSettlement(
	parent *processState,
	effectID EffectID,
	result ChildStartResult,
) (SettlementStatus, error) {
	_, record := parent.prepared.pendingEffect(effectID)
	if record == nil {
		return SettlementStatusInvalid, errors.New("pending child-start Effect is missing")
	}
	if err := record.settleChildStart(result); err != nil {
		return SettlementStatusInvalid, err
	}
	return record.Settlement.Status(), nil
}

func (t *treeRuntime) failPreparedEffect(process *processState, code string, err error) {
	process.recordFailure(FailureKindContract, code, err)
	if process.prepared != nil {
		t.terminatePrepared(process)
	} else {
		t.installTermination(process, stepOutcome{})
	}
	t.finishIfTerminal(process)
}

func (t *treeRuntime) applyStepCompletion(
	process *processState,
	job *processJob,
	result stepJobResult,
) {
	stepStatus := StepStatusSucceeded
	if result.err != nil {
		stepStatus = StepStatusFailed
	}
	durationMS := time.Since(job.startedAt).Milliseconds()
	payload, _ := json.Marshal(stepFinishedEventPayload{
		StepStatus: stepStatus,
		DurationMS: &durationMS,
	})
	sequence := process.committedSteps + 1
	t.publishEvent(process, EventStepFinished, EventPhaseAttempt, sequence, EffectID{}, payload)
	if result.err != nil {
		code := "execution.step.failed"
		switch result.stage {
		case stepJobStageSnapshot:
			code = "execution.snapshot.failed"
		case stepJobStageRestore:
			code = "execution.snapshot.unrestorable"
		}
		t.failProcess(process, failureKindForError(result.err), code, result.err)
		return
	}
	if failure := process.prepareStepResult(result); failure != nil {
		t.failProcess(process, failure.kind, failure.code, failure.cause)
		return
	}
	t.publishEphemeralStatus(process)
	t.publishEvent(process, EventStepPrepared, EventPhaseAttempt, sequence, EffectID{}, emptyEventPayload())
}

func (t *treeRuntime) applyDispatchCompletion(
	process *processState,
	job *processJob,
	result dispatchJobResult,
) {
	index, record := process.prepared.pendingEffect(result.effectID)
	if record == nil {
		return
	}
	settlement := result.settlement
	if err := record.settle(settlement); err != nil {
		t.failPreparedEffect(process, "engine.effect.settlement.invalid", err)
		return
	}
	var events []Event
	if result.dropped > 0 {
		process.usage.DroppedDeltas = saturatingCountAdd(
			process.usage.DroppedDeltas,
			result.dropped,
		)
		t.publishEphemeralStatus(process)
		payload, _ := json.Marshal(deltaDroppedEventPayload{DroppedDeltaCount: result.dropped})
		if t.engine.durability != nil {
			if event, ok := t.prepareEvent(process,
				EventDeltaDropped, EventPhaseAttempt,
				process.prepared.StepSequence, record.ID, payload,
			); ok {
				events = append(events, event)
			}
		} else {
			t.publishEvent(process, EventDeltaDropped, EventPhaseAttempt,
				process.prepared.StepSequence, record.ID, payload,
			)
		}
	}
	if t.engine.durability != nil {
		if event, ok := t.prepareSettlementEvent(process,
			record.ID, EffectTargetDispatcher, settlement.Status(), job.startedAt, result.err,
		); ok {
			events = append(events, event)
		}
		snapshot, err := t.captureTree()
		if err != nil {
			t.failDurability(err, process.handle.processID, record.ID)
			return
		}
		request := t.effectRequestFor(process, uint32(index), *record)
		boundary, err := newEffectBoundary(
			EffectBoundarySettled, request, settlement, t.head.digest(), snapshot,
		)
		if err != nil {
			t.failDurability(err, process.handle.processID, record.ID)
			return
		}
		commit := &treeCommit{
			kind: treeCommitEffectSettled, processID: process.handle.processID,
			effectID: record.ID, snapshot: snapshot, events: events,
		}
		t.startEffectCommit(commit, boundary)
		return
	}
	t.publishSettlementEvent(process, record.ID,
		EffectTargetDispatcher,
		settlement.Status(),
		job.startedAt, result.err,
	)
}

// A result can precede descendant cleanup. Publish each join once, from leaves
// upward, only after acknowledged outcomes and the owned calls have returned.
func (t *treeRuntime) publishJoins() bool {
	if t.commit != nil || t.freeze != nil {
		return false
	}
	changed := false
	for len(t.joinCandidates) != 0 {
		processes := orderedProcesses(t.joinCandidates)
		for index := len(processes) - 1; index >= 0; index-- {
			process := processes[index]
			delete(t.joinCandidates, process.handle.processID)
			changed = t.publishJoin(process) || changed
		}
	}
	return changed
}

func (t *treeRuntime) publishJoin(process *processState) bool {
	if process.handle.joinDone() {
		return false
	}
	if t.jobs[process.handle.processID] != nil {
		return false
	}
	select {
	case <-process.handle.bookkeepingDone:
	default:
		return false
	}
	_, outcomeErr := process.handle.outcome()
	failure, failed := errors.AsType[*RuntimeError](outcomeErr)
	var unresolved []EffectID
	if failed {
		unresolved = append(unresolved, failure.UnresolvedEffectIDs()...)
	}
	ready := true
	for _, childID := range t.childrenByParent[process.handle.processID] {
		child := t.processes[childID]
		select {
		case <-child.handle.joined:
			if childFailure, ok := errors.AsType[*RuntimeError](child.handle.joinError()); ok {
				failed = true
				unresolved = append(unresolved, childFailure.UnresolvedEffectIDs()...)
			}
		default:
			ready = false
		}
	}
	if !ready {
		return false
	}
	var joinErr *RuntimeError
	if failed {
		joinErr = &RuntimeError{
			processID: process.handle.processID, incarnationID: t.incarnation,
			headDigest: t.head.digest(), unresolvedEffectIDs: canonicalEffectIDs(unresolved), cause: t.fault,
		}
	}
	process.handle.finishJoin(joinErr)
	if joinErr == nil {
		t.notifyChildWaits(process.handle.processID, ChildWaitBoundaryDrained)
	}
	if parentID, child := process.handle.relation.ParentID(); child {
		t.queueJoin(t.processes[parentID])
	}
	return true
}

// Only changes to result publication, owned calls, or child joins can make a
// Process joinable. A failed check sleeps until one of those facts changes.
func (t *treeRuntime) queueJoin(process *processState) {
	if process == nil || process.handle.joinDone() {
		return
	}
	select {
	case <-process.handle.bookkeepingDone:
		t.joinCandidates[process.handle.processID] = process
	default:
	}
}

func (t *treeRuntime) completeProcessBookkeeping(process *processState) {
	process.handle.finishBookkeeping()
	t.queueJoin(process)
}

func (t *treeRuntime) finishIfTerminal(process *processState) {
	if t.fault != nil || process == nil || !process.status.Terminal() {
		return
	}
	if t.engine.durability != nil {
		t.stageTerminal(process)
		return
	}
	select {
	case <-process.handle.outcomePublished:
		return
	default:
	}
	t.publishEvent(process, EventProcessFinished, EventPhaseCommitted, 0, EffectID{},
		process.terminalEventPayload(),
	)
	process.handle.publishResult(process.result())
	t.propagateProcessTermination(process)
	t.completeProcessBookkeeping(process)
}

func (t *treeRuntime) propagateProcessTermination(process *processState) {
	processID := process.handle.processID
	delete(t.childWaits, processID)
	t.stopProcessTree(process)
	t.notifyChildWaits(processID, ChildWaitBoundaryResult)
}

func (t *treeRuntime) notifyChildWaits(processID ProcessID, boundary ChildWaitBoundary) {
	if t.fault != nil {
		return
	}
	child := t.processes[processID]
	if child == nil {
		return
	}
	parentID, hasParent := child.handle.relation.ParentID()
	if !hasParent {
		return
	}
	parent := t.processes[parentID]
	for _, registration := range orderedChildWaitRegistrations(t.childWaits[parentID]) {
		if registration.spec.Boundary != boundary || !containsProcessID(registration.spec.Children, processID) {
			continue
		}
		if parent == nil || parent.status.Terminal() || parent.pendingControl.hasTerminalIntent() ||
			parent.mailbox.contains(registration.waitID.childWaitSignalID()) {
			continue
		}
		outcomes, satisfied := t.childWaitOutcomes(registration)
		if !satisfied {
			continue
		}
		signal, err := encodeChildWaitSatisfied(registration.waitID, registration.spec.Key, boundary, outcomes)
		if err != nil {
			parent.recordFailure(FailureKindExecution, "engine.child.wait.satisfaction.encoding_failed", err)
			t.stopProcessTree(parent)
			continue
		}
		if t.deliverChildWaitSatisfied(parent, signal) {
			t.enqueueProcess(parent.handle.processID)
		} else if parent.pendingControl.hasTerminalIntent() {
			t.stopProcessTree(parent)
		}
	}
}

func (t *treeRuntime) childWaitOutcomes(
	registration *childWaitRegistration,
) ([]ChildOutcome, bool) {
	outcomes := make([]ChildOutcome, 0, len(registration.spec.Children))
	for _, childID := range registration.spec.Children {
		child := t.processes[childID]
		if child == nil {
			return nil, false
		}
		ready := child.status.Terminal()
		if registration.spec.Boundary == ChildWaitBoundaryDrained {
			ready = child.handle.joinDone() && child.handle.joinError() == nil
		}
		if ready {
			key, _ := child.handle.relation.ChildKey()
			outcomes = append(outcomes, ChildOutcome{key: key, result: child.result()})
		}
	}
	required, err := registration.spec.Condition.required(len(registration.spec.Children))
	return outcomes, err == nil && uint32(len(outcomes)) >= required
}

func (t *treeRuntime) registerChildWait(
	parentID ProcessID,
	waitID WaitID,
	spec ChildWaitSpec,
) (Signal, bool, error) {
	if !parentID.Valid() || !waitID.Valid() || !spec.Valid() || t.processes[parentID] == nil {
		return Signal{}, false, ErrInvalidChildWait
	}
	if t.childWaits[parentID][waitID] != nil {
		return Signal{}, false, ErrInvalidChildWait
	}
	for _, childID := range spec.Children {
		child := t.processes[childID]
		if child == nil {
			return Signal{}, false, ErrInvalidChildWait
		}
		actualParent, isChild := child.handle.relation.ParentID()
		if !isChild || actualParent != parentID {
			return Signal{}, false, ErrInvalidChildWait
		}
	}
	registration := &childWaitRegistration{
		waitID: waitID,
		spec:   spec.clone(),
	}
	if t.childWaits[parentID] == nil {
		t.childWaits[parentID] = make(map[WaitID]*childWaitRegistration)
	}
	t.childWaits[parentID][waitID] = registration
	outcomes, satisfied := t.childWaitOutcomes(registration)
	if !satisfied {
		return Signal{}, false, nil
	}
	signal, err := encodeChildWaitSatisfied(waitID, spec.Key, spec.Boundary, outcomes)
	if err != nil {
		t.unregisterChildWait(parentID, waitID)
		return Signal{}, false, err
	}
	return signal, true, nil
}

func (t *treeRuntime) unregisterChildWait(parentID ProcessID, waitID WaitID) {
	registrations := t.childWaits[parentID]
	delete(registrations, waitID)
	if len(registrations) == 0 {
		delete(t.childWaits, parentID)
	}
}

func (t *treeRuntime) addProcess(process *processState) {
	if t == nil || process == nil || process.handle == nil ||
		process.handle.relation.RootID() != t.rootID {
		panic("agent: invalid tree Process")
	}
	processID := process.handle.processID
	if t.processes[processID] != nil {
		panic("agent: duplicate tree Process")
	}
	process.handle.runtime.Store(t)
	t.processes[processID] = process
	t.queueJoin(process)
	if parentID, child := process.handle.relation.ParentID(); child {
		t.childrenByParent[parentID] = append(t.childrenByParent[parentID], processID)
	}
	if !process.status.Terminal() {
		t.enqueueProcess(processID)
	}
}

// Only unpublished children can be removed while the owner is running. Every
// membership change updates the derived parent index at this boundary.
func (t *treeRuntime) removeProcess(processID ProcessID) {
	process := t.processes[processID]
	if process == nil {
		return
	}
	if parentID, child := process.handle.relation.ParentID(); child {
		children := t.childrenByParent[parentID]
		index := slices.Index(children, processID)
		if index < 0 {
			panic("agent: tree child membership is missing")
		}
		children = slices.Delete(children, index, index+1)
		if len(children) == 0 {
			delete(t.childrenByParent, parentID)
		} else {
			t.childrenByParent[parentID] = children
		}
	}
	delete(t.processes, processID)
	delete(t.joinCandidates, processID)
}

func (t *treeRuntime) acquireTreeFreeze(
	ctx context.Context,
) (*treeFreeze, TreeSnapshot, error) {
	ctx = requireContext(ctx)
	if err := ctx.Err(); err != nil {
		return nil, TreeSnapshot{}, err
	}
	if snapshot, err, stopped := t.captureStoppedTree(); stopped {
		return nil, snapshot, err
	}
	acquisition := &treeFreezeAcquisition{
		response: make(chan treeFreezeAcquisitionResult, 1),
		canceled: make(chan struct{}),
	}
	select {
	case t.controls <- treeCommand{
		kind: treeCommandAcquireFreeze, acquisition: acquisition,
	}:
	case <-t.done:
		snapshot, err, _ := t.captureStoppedTree()
		return nil, snapshot, err
	case <-ctx.Done():
		return nil, TreeSnapshot{}, ctx.Err()
	}
	select {
	case result := <-acquisition.response:
		return result.freeze, result.snapshot, result.err
	case <-t.done:
		snapshot, err, _ := t.captureStoppedTree()
		return nil, snapshot, err
	case <-ctx.Done():
		close(acquisition.canceled)
		return nil, TreeSnapshot{}, ctx.Err()
	}
}

func (t *treeRuntime) captureStoppedTree() (TreeSnapshot, error, bool) {
	select {
	case <-t.done:
		snapshot, err := t.captureTree()
		return snapshot, err, true
	default:
		return TreeSnapshot{}, nil, false
	}
}

// The tree owner installs wait registrations around one candidate adoption.
// A rejected local transition cannot leave a live registration behind.
func (t *treeRuntime) finalizePrepared(process *processState) error {
	finalization, err := newPreparedStepFinalization(process)
	if err != nil {
		return err
	}
	if err := finalization.prepareSettlements(); err != nil {
		return err
	}
	var registered []WaitID
	adopted := false
	defer func() {
		if !adopted {
			for _, waitID := range registered {
				t.unregisterChildWait(process.handle.processID, waitID)
			}
		}
	}()
	for _, opened := range finalization.openedChildWaits {
		signal, satisfied, err := t.registerChildWait(process.handle.processID, opened.WaitID(), opened.Spec())
		if err != nil {
			return err
		}
		registered = append(registered, opened.WaitID())
		if satisfied {
			finalization.immediateChildSignals = append(finalization.immediateChildSignals, signal)
		}
	}
	if err := finalization.enqueueImmediateChildSignals(); err != nil {
		return err
	}
	if err := finalization.prepareTransition(time.Now().Round(0).UTC()); err != nil {
		return err
	}
	finalization.adopt()
	adopted = true
	for _, waitID := range finalization.consumedChildWaits {
		t.unregisterChildWait(process.handle.processID, waitID)
	}
	for _, waitID := range finalization.transition.closedChildWaits {
		t.unregisterChildWait(process.handle.processID, waitID)
	}
	t.publishEphemeralStatus(process)
	payload, _ := json.Marshal(stepCommittedEventPayload{ProcessStatus: process.status})
	t.publishEventAfterCommit(process, EventStepCommitted, EventPhaseCommitted, process.committedSteps, EffectID{}, payload)
	if process.status == StatusPaused {
		t.publishEventAfterCommit(process, EventProcessPaused, EventPhaseCommitted, 0, EffectID{}, emptyEventPayload())
	}
	return nil
}

func (t *treeRuntime) terminatePrepared(process *processState) {
	process.preparedExecution = nil
	process.execution = nil
	t.installTerminationWithUnresolved(process, stepOutcome{}, process.unknownEffectIDs())
}

func (t *treeRuntime) failProcess(process *processState, kind FailureKind, code string, err error) {
	process.recordFailure(kind, code, err)
	t.installTermination(process, stepOutcome{})
}

func (t *treeRuntime) installTermination(process *processState, outcome stepOutcome) {
	t.installTerminationWithUnresolved(process, outcome, nil)
}

func (t *treeRuntime) installTerminationWithUnresolved(process *processState, outcome stepOutcome, unresolvedEffectIDs []EffectID) {
	termination := process.resolveStepTermination(outcome)
	process.installTermination(termination.withUnresolvedEffectIDs(unresolvedEffectIDs), Output{}, time.Now().Round(0).UTC())
	for _, waitID := range process.mailbox.closeAllWaits() {
		t.unregisterChildWait(process.handle.processID, waitID)
	}
	t.publishEphemeralStatus(process)
}

func emptyEventPayload() json.RawMessage { return json.RawMessage("{}") }
