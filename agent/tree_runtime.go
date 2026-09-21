package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"sync"
	"sync/atomic"
	"time"
)

// treeRuntime serializes authoritative changes because sibling jobs must not
// publish incompatible tree cuts. Fenced completions let computation and dispatch
// run concurrently without sharing commit authority.
//
// run owns command/completion ordering; advanceOne grants worker permission.
// setTreeCommit/applySuccessfulTreeCommit keep acknowledgment ahead of publication.
// advancePrepared finalizes Effects; completeFreeze and publishJoins expose
// quiescence and drainage without creating another state-transition owner.
type treeRuntime struct {
	// Incarnation and head travel together to prevent a retired writer from
	// advancing the current tree.
	engine         *Engine
	rootID         ProcessID
	incarnation    TreeIncarnationID
	head           TreeSnapshot
	commitSequence uint64

	// External readers need scheduling liveness without acquiring execution
	// state. Atomics expose that view while commands and completions preserve
	// one mutation owner.
	inFlightWork    atomic.Int64
	freezeActive    atomic.Bool
	context         context.Context
	processCommands chan treeCommand
	freezeCommands  chan treeCommand
	completions     chan treeJobCompletion
	inspections     chan chan treeInspectionResponse

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
	kind          processJobKind
	attempt       processAttempt
	cancel        context.CancelFunc
	stale         bool
	effectID      EffectID
	childStart    *childStartPlan
	effectAttempt effectAttempt
	response      chan processResponse
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
	treeCommitChildStart
	treeCommitCheckpoint
	treeCommitSignals
)

type treeCommit struct {
	kind      treeCommitKind
	processID ProcessID
	effectID  EffectID
	snapshot  TreeSnapshot
	response  chan processResponse
	events    []eventFact
	child     *pendingChildStartPublication
}

func (t *treeCommit) reply(response processResponse) {
	if t.response != nil {
		t.response <- response
	}
}

type pendingChildStartPublication struct {
	parentID      ProcessID
	effectID      EffectID
	plan          *childStartPlan
	result        childStartJobResult
	effectAttempt effectAttempt
	event         eventFact
}

func (p *pendingChildStartPublication) childSettlementStatus() SettlementStatus {
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
	events   []eventFact
	terminal bool
}

type stepJobResult struct {
	finishedAt       time.Time
	workDuration     time.Duration
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
		incarnation:         newTreeIncarnationID(),
		context:             context.WithoutCancel(RequireContext(ctx)),
		processCommands:     make(chan treeCommand, treeCommandBufferCapacity),
		freezeCommands:      make(chan treeCommand, treeCommandBufferCapacity),
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

func (t *treeRuntime) establishHead(
	incarnation TreeIncarnationID,
	snapshot TreeSnapshot,
) {
	if t == nil || !incarnation.Valid() || !snapshot.Valid() ||
		snapshot.RootID() != t.rootID {
		panic("agent: invalid durable tree head")
	}
	snapshotIncarnation := snapshot.IncarnationID()
	if snapshotIncarnation != incarnation {
		panic("agent: durable tree head incarnation mismatch")
	}
	t.incarnation = incarnation
	t.head = snapshot
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
		if advanced {
			t.tryInspection()
		} else {
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
		case t.processCommands <- newTreeProcessCommand(
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
	lanes := [...]bool{
		t.tryCommitCompletion(),
		t.tryFreezeCancellation(),
		t.tryFreezeCommand(),
		t.tryProcessCommand(),
		t.tryCompletion(),
		t.advanceOne(),
		t.publishJoins(),
		t.tryStartCheckpoint(),
	}
	return slices.Contains(lanes[:], true)
}

func (t *treeRuntime) waitForWork() {
	// Nil channels disable blocked lanes without duplicating event dispatch.
	// These are local selections; only the owner changes the actual barriers.
	commitDone := t.commitDone
	freezeCommands := t.freezeCommands
	processCommands := t.processCommands
	completions := t.completions
	freezeCancellation := t.freezeCancellation()
	if t.mutationsBlocked() {
		processCommands = nil
		completions = nil
	}
	if t.commit != nil {
		freezeCommands = nil
		freezeCancellation = nil
	} else {
		commitDone = nil
	}
	// Read-only queries cannot make checkpoint or scheduling work ready.
	for {
		select {
		case completion := <-commitDone:
			t.applyTreeCommitCompletion(completion)
		case command := <-freezeCommands:
			t.applyCommand(command)
		case response := <-t.inspections:
			t.replyInspection(response)
			continue
		case command := <-processCommands:
			t.applyCommand(command)
		case completion := <-completions:
			t.applyCompletion(completion)
		case <-freezeCancellation:
			t.releaseCurrentFreeze()
		}
		return
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

func (t *treeRuntime) freezeCancellation() <-chan struct{} {
	if t.freeze == nil || t.freeze.acquisition == nil {
		return nil
	}
	return t.freeze.acquisition.canceled
}

func (t *treeRuntime) tryFreezeCancellation() bool {
	canceled := t.freezeCancellation()
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

func (t *treeRuntime) tryFreezeCommand() bool {
	if t.commit != nil {
		return false
	}
	select {
	case command := <-t.freezeCommands:
		t.applyCommand(command)
		return true
	default:
		return false
	}
}

func (t *treeRuntime) tryProcessCommand() bool {
	if t.mutationsBlocked() {
		return false
	}
	select {
	case command := <-t.processCommands:
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
		t.failProcessContract(process, failureCodeEngineEffectPhaseInvalid, err)
		return
	}
	if process.pendingControl.hasTerminalIntent() {
		t.stopProcessTree(process)
		t.terminatePreparedProcess(process)
		t.finishIfTerminal(process)
		return
	}
	if process.pendingControl.pauseReason != "" && process.prepared.Intent.Kind() == TransitionKindWait {
		process.prepared = nil
		process.preparedExecution = nil
		t.startRestore(process)
		t.applyPendingControl(process)
		return
	}
	if record != nil {
		if record.unknown() {
			return
		}
		t.startPreparedEffect(process, index, record)
		return
	}
	checkpoint := process.prepared.Intent.Kind() == TransitionKindCheckpoint
	if err := t.finalizePrepared(process); err != nil {
		if errors.Is(err, ErrResourceLimitExceeded) {
			process.recordFailure(FailureKindExecution, failureCodeEngineLimitChildWaitSignal, err)
		} else {
			process.recordFailure(FailureKindContract, failureCodeEngineFinalizeInvalid, err)
		}
		t.terminatePreparedProcess(process)
	}
	t.finishIfTerminal(process)
	if !process.status.Terminal() {
		t.enqueueProcess(process.handle.processID)
		if checkpoint {
			snapshot, err := t.captureTree()
			if err == nil {
				err = t.startCheckpointCommit(t.checkpointKind(), snapshot)
			}
			if err != nil {
				t.failRuntime(err, process.handle.processID, EffectID{})
			}
		}
	}
}

func (t *treeRuntime) allocateAttempt(process *processState) (processAttempt, bool) {
	if process.attemptSequence == math.MaxUint64 {
		t.failProcess(process,
			FailureKindContract,
			failureCodeEngineProcessAttemptExhausted,
			errors.New("process attempt sequence is exhausted"),
		)
		t.finishIfTerminal(process)
		return 0, false
	}
	process.attemptSequence++
	return processAttempt(process.attemptSequence), true
}

func (t *treeRuntime) setProcessJob(processID ProcessID, job *processJob) {
	if !processID.Valid() || t.processes[processID] == nil || job == nil || t.jobs[processID] != nil {
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
			spec, FailureKindContract, failureCodeEngineChildRequestInvalid, ErrInvalidChildStart,
		)}
	}
	childID := effectID.childProcessID()
	relation := childProcessRelation(childID, process.handle.relation, spec.Key)
	requestDigest, err := spec.digest()
	if err != nil {
		return childStartPreparation{result: failedChildStart(
			spec, FailureKindContract, failureCodeEngineChildRequestInvalid, err,
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
			spec, FailureKindContract, failureCodeEngineChildIdentityConflict, ErrInvalidChildStart,
		)}
	}
	if !process.capabilities.Allows(spec.Capabilities) {
		return childStartPreparation{result: failedChildStart(
			spec, FailureKindContract, failureCodeEngineChildCapabilityEscalation, ErrInvalidCapability,
		)}
	}
	if !t.canStartChild(process) {
		return childStartPreparation{result: failedChildStart(
			spec, FailureKindExecution, failureCodeEngineChildTreeLimit, ErrResourceLimitExceeded,
		)}
	}
	if !process.reserveProvisionalChildBudget(spec.Budget) {
		return childStartPreparation{result: failedChildStart(
			spec, FailureKindExecution, failureCodeEngineChildBudgetExhausted, ErrResourceLimitExceeded,
		)}
	}
	transferred := false
	defer func() {
		if !transferred {
			process.releaseProvisionalChildBudget(spec.Budget)
		}
	}()
	childLimits := process.limits
	childLimits.Budget = spec.Budget
	if reserveProcessStartErr := t.engine.reserveProcessStart(
		relation, spec.DeploymentRef, process.treeLimits, requestDigest,
	); reserveProcessStartErr != nil {
		if errors.Is(reserveProcessStartErr, ErrResourceLimitExceeded) {
			return childStartPreparation{result: failedChildStart(
				spec, FailureKindExecution, failureCodeEngineChildTreeLimit, reserveProcessStartErr,
			)}
		}
		if errors.Is(reserveProcessStartErr, ErrEngineClosed) {
			return childStartPreparation{result: failedChildStart(
				spec, FailureKindExternal, failureCodeEngineChildStartUnavailable, reserveProcessStartErr,
			)}
		}
		return childStartPreparation{result: failedChildStart(
			spec, FailureKindContract, failureCodeEngineChildIdentityConflict, reserveProcessStartErr,
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

// Membership and in-flight child jobs are the resource facts. A completed job
// installs its child before scheduling resumes; durable publication blocks that
// scheduling lane until acknowledgment. No parallel reservation counter exists.
func (t *treeRuntime) canStartChild(parent *processState) bool {
	limits := parent.treeLimits
	if parent.handle.relation.Depth() >= limits.MaxDepth {
		return false
	}
	children := t.childrenByParent[parent.handle.processID]
	childCount, treeCount := uint64(len(children)), uint64(len(t.processes))
	var active uint64
	for _, childID := range children {
		if !t.processes[childID].status.Terminal() {
			active++
		}
	}
	for _, job := range t.jobs {
		if job.childStart == nil || t.processes[job.childStart.childID] != nil {
			continue
		}
		treeCount++
		if parentID, _ := job.childStart.relation.ParentID(); parentID == parent.handle.processID {
			childCount++
			active++
		}
	}
	return limits.MaxChildren.Allows(childCount, 1) && active < uint64(limits.MaxActiveChildren) &&
		limits.MaxTreeProcesses.Allows(treeCount, 1)
}

// A tree-local control and its receipt change one authoritative cut. The target
// cannot consume the new input before that cut is acknowledged.
func (t *treeRuntime) controlChild(parent *processState, index uint32, record *preparedEffect, observation effectAttempt) {
	request, err := decodeChildControlEffect(record.Effect.Payload())
	if err != nil {
		t.failProcessContract(parent, failureCodeEngineChildControlInvalid, err)
		return
	}
	result := request.result()
	child := t.processes[request.ChildID]
	if child == nil || child.handle.relation.parentID != parent.handle.processID {
		result.failure = newEngineFailure(FailureKindContract, failureCodeEngineChildControlNotOwned,
			errors.New("control recipient is not a direct child"))
	} else {
		result = t.applyChildControl(child, request)
	}
	if settlementErr := record.settleChildControl(result); settlementErr != nil {
		t.failProcessContract(parent, failureCodeEngineChildControlSettlementInvalid, settlementErr)
		return
	}
	t.publishSettlementEvent(parent, record.ID, EffectTargetFramework, record.Settlement.Status(), observation, nil)
	t.enqueueProcess(parent.handle.processID)

	snapshot, err := t.captureTree()
	if err != nil {
		t.failRuntime(err, parent.handle.processID, record.ID)
		return
	}
	boundary, err := newEffectBoundary(t.commitSequence+1, EffectBoundaryKindSettled, t.effectRequestFor(parent, index, *record),
		*record.Settlement, t.head.Digest(), snapshot)
	if err != nil {
		t.failRuntime(err, parent.handle.processID, record.ID)
		return
	}
	t.startEffectCommit(&treeCommit{
		kind: treeCommitEffectSettled, processID: parent.handle.processID,
		effectID: record.ID, snapshot: snapshot,
	}, boundary)
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
		result.failure = newEngineFailure(FailureKindContract, failureCodeEngineChildSignalRejected, err)
		return result
	}
	events, err := t.admitSignals(child, []Signal{signal}, signalSourceExternal)
	if err != nil {
		result.failure = newEngineFailure(FailureKindExecution, failureCodeEngineChildSignalRejected, err)
		return result
	}
	if len(events) > 0 {
		for _, event := range events {
			t.stageCommittedEvent(event)
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
	boundary, err := newEffectBoundary(t.commitSequence+1,
		EffectBoundaryKindPending, request, Settlement{}, t.head.Digest(), snapshot,
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
	if t.commit != nil || !boundary.kind.Valid() ||
		commit == nil || !commit.processID.Valid() {
		panic("agent: invalid concurrent tree commit")
	}
	t.setTreeCommit(commit)
	go func() {
		err := commitEffectBoundary(t.context, t.engine.committer, boundary)
		t.commitDone <- treeCommitCompletion{commit: commit, err: err}
	}()
}

func (t *treeRuntime) startUnknownResolutionCommit(
	process *processState,
	command processCommand,
	index int,
) error {
	record := &process.prepared.Effects[index]
	snapshot, err := t.captureTree()
	if err != nil {
		return err
	}
	request := t.effectRequestFor(process, uint32(index), *record)
	boundary, err := newEffectBoundary(t.commitSequence+1,
		EffectBoundaryKindResolved, request, command.settlement, t.head.Digest(), snapshot,
	)
	if err != nil {
		return err
	}
	commit := &treeCommit{
		kind: treeCommitEffectResolved, processID: process.handle.processID,
		effectID: record.ID, snapshot: snapshot,
		response: command.response,
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

func (t *treeRuntime) startSignalCommit(process *processState, command processCommand, events []eventFact) error {
	snapshot, err := t.captureTree()
	if err != nil {
		return err
	}
	return t.startCheckpoint(&treeCommit{
		kind: treeCommitSignals, processID: process.handle.processID,
		snapshot: snapshot, response: command.response, events: events,
	}, TreeCheckpointKindSignals)
}

func (t *treeRuntime) startCheckpoint(commit *treeCommit, kind TreeCheckpointKind) error {
	checkpoint, err := newTreeCheckpoint(t.commitSequence+1, kind, t.head.Digest(), commit.snapshot)
	if err != nil {
		return err
	}
	if t.commit != nil {
		return errors.New("invalid concurrent tree checkpoint")
	}
	t.setTreeCommit(commit)
	go func() {
		err := commitTreeCheckpoint(t.context, t.engine.committer, checkpoint)
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
	commit.reply(processResponse{err: commitErr})
	unresolvedEffectID := commit.effectID
	if commit.kind == treeCommitEffectPending {
		unresolvedEffectID = EffectID{}
	}
	if commit.kind == treeCommitChildStart {
		t.discardChildStart(commit.child.plan)
	}
	t.failRuntime(commitErr, commit.processID, unresolvedEffectID)
}

func (t *treeRuntime) applySuccessfulTreeCommit(commit *treeCommit) {
	defer t.completeFreeze()
	if commit.snapshot.Valid() {
		t.head = commit.snapshot
		t.commitSequence++
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
		commit.reply(processResponse{})
		t.enqueueProcess(commit.processID)
	case treeCommitChildStart:
		if err := t.publishChildStart(commit.child); err != nil {
			t.discardChildStart(commit.child.plan)
			t.failRuntime(err, commit.processID, commit.effectID)
			return
		}
		t.enqueueProcess(commit.processID)
	case treeCommitCheckpoint:
	case treeCommitSignals:
		commit.reply(processResponse{accepted: true})
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
		if parent != nil {
			parent.releaseCommittedChildBudget(plan.spec.Budget)
		}
		t.removeProcess(plan.childID)
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
	t.engine.discardProcessStart(plan.childID)
}

func (t *treeRuntime) publishChildStart(pending *pendingChildStartPublication) error {
	if pending == nil || pending.plan == nil {
		return errors.New("child start publication is incomplete")
	}
	if pending.result.started() {
		child := t.processes[pending.plan.childID]
		if child == nil {
			return errors.New("started child is missing from prospective tree")
		}
		t.engine.publishProcessStart(child.handle)
		t.publishEvent(child, EventProcessStarted, EventPhaseCommitted, 0, EffectID{}, emptyEventPayload())
	}
	parent := t.processes[pending.parentID]
	if pending.event.processID.Valid() {
		t.publishPreparedEvent(parent, pending.event)
	} else {
		t.publishSettlementEvent(parent, pending.effectID, EffectTargetFramework,
			pending.childSettlementStatus(), pending.effectAttempt, nil,
		)
	}
	return nil
}

func (t *treeRuntime) tryStartCheckpoint() bool {
	if t.fault != nil || t.commit != nil ||
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
		t.failRuntime(err, ProcessID{}, EffectID{})
		return true
	}
	if snapshot.Digest() == t.head.Digest() {
		// Control changes can return to the acknowledged state without changing
		// its recovery cut. Publishing those facts must not require another write.
		pending := len(t.pendingPublications) != 0
		t.publishAcknowledgedChanges()
		return pending
	}
	if err := t.startCheckpointCommit(kind, snapshot); err != nil {
		t.failRuntime(err, ProcessID{}, EffectID{})
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
		return TreeCheckpointKindProgress
	}
	if allTerminal {
		return TreeCheckpointKindTerminal
	}
	return TreeCheckpointKindParked
}

func (t *treeRuntime) stageTerminal(process *processState) {
	if process == nil || !process.status.Terminal() {
		return
	}
	select {
	case <-process.handle.outcomePublished:
		return
	default:
	}
	processID := process.handle.processID
	publication := t.pendingPublications[processID]
	if publication.terminal {
		return
	}
	payload := process.terminalEventPayload()
	event := t.prepareEvent(process,
		EventProcessFinished, EventPhaseCommitted, 0, EffectID{}, payload,
	)
	t.propagateProcessTermination(process)
	publication.events = append(publication.events, event)
	publication.terminal = true
	t.pendingPublications[processID] = publication
}

func (t *treeRuntime) stageCommittedEvent(event eventFact) {
	if event.phase != EventPhaseCommitted || event.relation.RootID() != t.rootID ||
		t.processes[event.processID] == nil {
		panic("agent: invalid committed Event")
	}
	processID := event.processID
	publication := t.pendingPublications[processID]
	publication.events = append(publication.events, event)
	t.pendingPublications[processID] = publication
}

func (t *treeRuntime) publishAcknowledgedChanges() {
	for _, snapshot := range t.head.state.ProcessSnapshots {
		processID := snapshot.ProcessID()
		process := t.processes[processID]
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
		result, terminal := snapshot.Result()
		if !terminal {
			panic("agent: terminal publication requires an acknowledged outcome")
		}
		process.handle.publishResult(result)
		t.finishProcessBookkeeping(process)
		delete(t.pendingPublications, processID)
	}
}

func (t *treeRuntime) failRuntime(
	cause error,
	processID ProcessID,
	effectID EffectID,
) {
	if t.fault != nil {
		return
	}
	if !t.head.Valid() {
		panic("agent: committer failure requires an acknowledged tree")
	}
	t.fault = cause
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
	acknowledgedByID := make(map[ProcessID]struct{}, len(t.head.state.ProcessSnapshots))
	for _, snapshot := range t.head.state.ProcessSnapshots {
		acknowledgedByID[snapshot.ProcessID()] = struct{}{}
	}
	for _, process := range orderedProcesses(t.processes) {
		processID := process.handle.processID
		_, acknowledged := acknowledgedByID[processID]
		if !acknowledged {
			// A prospective child that never entered an acknowledged head has
			// no published lifecycle to stop.
			t.engine.discardProcessStart(processID)
			t.removeProcess(processID)
			continue
		}
		if !process.handle.publishRuntimeFailure(&RuntimeError{
			processID: processID, incarnationID: t.incarnation, headDigest: t.head.Digest(),
			unresolvedEffectIDs: canonicalEffectIDs(unresolvedByProcess[processID]), cause: cause,
		}) {
			continue
		}
		failure := newTreeRuntimeFailure(cause)
		payload := marshalEventPayload(runtimeStoppedEventPayload{
			FailureKind: failure.Kind(), FailureCode: failure.Code(),
		})
		t.publishEvent(process, EventRuntimeStopped, EventPhaseAttempt, 0, EffectID{}, payload)
		t.finishProcessBookkeeping(process)
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
	// A runtime fault prevents completion adoption; only job collection remains.
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
			command.response <- errors.New("agent: invalid tree command")
		}
		return
	}
	process := t.processes[command.processID]
	if process == nil {
		command.process.reply(processResponse{err: fmt.Errorf("%w: Process %q is not owned by this tree", ErrInvalidProcessControl, command.processID)})
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
			t.stageEvent(process, EventProcessResumed, EventPhaseCommitted, 0, EffectID{}, emptyEventPayload())
		}
		command.reply(processResponse{err: err})
	case commandCancel:
		process.requestCancellation(command.cancellationIntent)
	case commandKill:
		command.reply(processResponse{err: process.requestKill(command.reason)})
	case commandResolveUnknownEffect:
		t.resolveUnknownEffect(process, command)
		return
	case commandReplayUnknownEffect:
		t.replayUnknownEffect(process, command)
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
	if t.jobs[process.handle.processID] != nil {
		command.reply(processResponse{err: ErrEffectNotPending})
		return
	}
	t.commitResolution(process, command)
}

func (t *treeRuntime) commitResolution(process *processState, command processCommand) {
	candidate, index, err := process.prepareResolution(command.settlement)
	if err == nil {
		err = t.validateSnapshotCapacity(candidate)
	}
	if err != nil {
		command.reply(processResponse{err: err})
		return
	}
	process.adoptCandidate(candidate)
	record := process.prepared.Effects[index]
	payload := marshalEventPayload(effectResolvedEventPayload{
		EffectTarget: record.Effect.Target(), SettlementStatus: command.settlement.Status(),
	})
	t.stageEvent(process, EventEffectResolved, EventPhaseCommitted,
		process.prepared.StepSequence, record.ID, payload)

	if err := t.startUnknownResolutionCommit(process, command, index); err != nil {
		command.reply(processResponse{err: err})
		t.failRuntime(err, process.handle.processID, command.settlement.EffectID())
	}
}

func (t *treeRuntime) replayUnknownEffect(process *processState, command processCommand) {
	if process.pendingControl.hasTerminalIntent() {
		command.reply(processResponse{err: ErrProcessFinished})
		return
	}
	if process.prepared == nil || t.jobs[process.handle.processID] != nil {
		command.reply(processResponse{err: ErrEffectNotPending})
		return
	}
	index, record, err := process.prepared.nextEffect()
	if err != nil || record == nil || record.ID != command.effectID || !record.unknown() {
		command.reply(processResponse{err: ErrEffectNotPending})
		return
	}
	policy, err := dispatcherReplayPolicy(process.deployment.dispatcher, record.Effect)
	if err != nil || record.Effect.Target() != EffectTargetDispatcher || policy != ReplayPolicySameIdentity {
		command.reply(processResponse{err: errors.Join(ErrEffectReplayForbidden, err)})
		return
	}
	// The committed Unknown already retains the exact uncertain operation.
	// Keep it intact while the same logical operation is reconciled: revoking
	// an unused new attempt must never erase evidence of an earlier attempt.
	t.startDispatch(process, uint32(index), *record, command.response)
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
	if t.freeze == nil || t.freeze.ready || t.commit != nil || t.freezeBlockedByJob() {
		return
	}
	snapshot, err := t.captureTree()
	if err != nil {
		acquisition := t.freeze.acquisition
		t.releaseCurrentFreeze()
		acquisition.response <- treeFreezeAcquisitionResult{err: err}
		return
	}
	if snapshot.Digest() != t.head.Digest() {
		if err := t.startCheckpointCommit(t.checkpointKind(), snapshot); err != nil {
			t.failRuntime(err, ProcessID{}, EffectID{})
		}
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
		if !process.status.Terminal() {
			allTerminal = false
			break
		}
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
	wire := t.treeSnapshotBase()
	for _, process := range t.processes {
		snapshot, err := process.capture()
		if err != nil {
			return TreeSnapshot{}, err
		}
		wire.ProcessSnapshots = append(wire.ProcessSnapshots, snapshot)
	}
	return treeSnapshotFromWire(wire)
}

func (t *treeRuntime) treeSnapshotBase() treeSnapshotWire {
	wire := treeSnapshotWire{RootID: t.rootID, ProcessSnapshots: []ProcessSnapshot{}}
	wire.IncarnationID = t.incarnation

	for parentID, registrations := range t.childWaits {
		for _, registration := range registrations {
			wire.ChildWaits = append(wire.ChildWaits, childWaitSnapshotWire{ParentProcessID: parentID, WaitID: registration.waitID, Spec: registration.spec.wire()})
		}
	}
	return wire
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

	t.stageEvent(process, EventProcessPaused, EventPhaseCommitted, 0, EffectID{}, emptyEventPayload())
	return true
}

func (t *treeRuntime) deliverChildWaitSatisfied(process *processState, signal Signal) bool {
	events, err := t.admitSignals(process, []Signal{signal}, signalSourceChildWait)
	if err != nil {
		if errors.Is(err, ErrResourceLimitExceeded) {
			process.recordFailure(FailureKindExecution, failureCodeEngineLimitChildWaitSignal, err)
		} else {
			process.recordFailure(FailureKindContract, failureCodeEngineChildWaitSatisfactionInvalid, err)
		}
		return false
	}
	for _, event := range events {
		t.stageCommittedEvent(event)
	}
	return len(events) > 0 || process.mailbox.contains(signal.ID())

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
	events, err := t.admitSignals(process, signals, signalSourceExternal)
	if err != nil || len(events) == 0 {
		command.reply(processResponse{err: err})
		return
	}

	if err := t.startSignalCommit(process, command, events); err != nil {
		command.reply(processResponse{err: err})
		t.failRuntime(err, process.handle.processID, EffectID{})
	}
}

func (t *treeRuntime) publishEvent(
	process *processState,
	name string,
	phase EventPhase,
	step uint64,
	effectID EffectID,
	payload json.RawMessage,
) {
	event := t.prepareEvent(process, name, phase, step, effectID, payload)
	t.publishPreparedEvent(process, event)
}

func (t *treeRuntime) stageEvent(
	process *processState,
	name string,
	phase EventPhase,
	step uint64,
	effectID EffectID,
	payload json.RawMessage,
) {
	event := t.prepareEvent(process, name, phase, step, effectID, payload)
	t.stageCommittedEvent(event)
}

func (t *treeRuntime) prepareEvent(
	process *processState,
	name string,
	phase EventPhase,
	step uint64,
	effectID EffectID,
	payload json.RawMessage,
) eventFact {
	event, err := newEventFact(eventSpec{
		processID:     process.handle.processID,
		deploymentRef: process.deployment.DeploymentRef(),
		relation:      process.handle.relation,
		incarnationID: t.incarnation,
		stepSequence:  step,
		effectID:      effectID,
		name:          name,
		phase:         phase,
		occurredAt:    time.Now(),
		payload:       payload,
	})
	if err != nil {
		panic(err)
	}
	return event
}

func (t *treeRuntime) publishPreparedEvent(process *processState, event eventFact) {
	if process.processEventSequence == math.MaxUint64 {
		t.engine.observation.recordDroppedEvent()
		return
	}
	// Prepared facts may wait for a durable acknowledgment while newer attempts
	// are observed. Only publication establishes their listener-visible order.
	process.processEventSequence++
	t.engine.observation.publishEvent(context.WithoutCancel(t.context), event.publish(process.processEventSequence))
}

func (t *treeRuntime) beginEffectAttempt(
	process *processState,
	step uint64,
	effectID EffectID,
	target EffectTarget,
) effectAttempt {
	attempt := effectAttempt{id: newEffectAttemptID(), startedAt: time.Now()}
	payload := marshalEventPayload(effectStartedEventPayload{EffectTarget: target, AttemptID: attempt.id})
	t.publishEvent(process, EventEffectStarted, EventPhaseAttempt, step, effectID, payload)
	return attempt
}

func (t *treeRuntime) publishSettlementEvent(
	process *processState,
	effectID EffectID,
	target EffectTarget,
	status SettlementStatus,
	observation effectAttempt,
	cause error,
) {
	event := t.prepareSettlementEvent(process, effectID, target, status, observation, cause)
	t.publishPreparedEvent(process, event)
}

func (t *treeRuntime) prepareSettlementEvent(
	process *processState,
	effectID EffectID,
	target EffectTarget,
	status SettlementStatus,
	observation effectAttempt,
	cause error,
) eventFact {
	durationMS := time.Since(observation.startedAt).Milliseconds()
	failure := dispatchFailure(cause)
	failureKind, failureCode := failure.Kind(), failure.Code()
	payload := marshalEventPayload(effectFinishedEventPayload{
		EffectTarget: target, SettlementStatus: status, AttemptID: observation.id,
		DurationMS: &durationMS, FailureKind: failureKind, FailureCode: failureCode,
	})
	return t.prepareEvent(process,
		EventEffectFinished, EventPhaseAttempt,
		process.prepared.StepSequence, effectID, payload,
	)
}

func (t *treeRuntime) prepareSignalEvents(process *processState, records []signalRecord) []eventFact {
	var events []eventFact
	for _, record := range records {
		payload := marshalEventPayload(signalAcceptedEventPayload{SignalID: record.id.String(), WaitID: record.waitID.String()})
		events = append(events, t.prepareEvent(process, EventSignalAccepted, EventPhaseCommitted, 0, EffectID{}, payload))
	}
	return events
}

func (t *treeRuntime) checkEventListenerReentrancy(ctx context.Context, operation string) error {
	return t.engine.observation.checkEventListenerReentrancy(ctx, t.rootID, operation)
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
		RootID: t.rootID, IncarnationID: t.incarnation, HeadDigest: t.head.Digest(),
		CommitPending: t.commit != nil, Freeze: TreeFreezePhaseNone,
	}
	if t.freeze != nil {
		inspection.Freeze = TreeFreezePhaseAcquiring
		if t.freeze.ready {
			inspection.Freeze = TreeFreezePhaseHeld
		}
	}
	snapshots := t.head.ProcessSnapshots()
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
	processID := process.handle.processID
	definition := process.deployment.Definition()

	if failure := process.stepSchedulingFailure(); failure != nil {
		t.failProcess(process, failure.kind, failure.code, failure.cause)
		t.finishIfTerminal(process)
		return
	}
	attempt, ok := t.allocateAttempt(process)
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
	t.setProcessJob(processID, &processJob{
		kind: processJobStep, attempt: attempt, cancel: cancel,
	})
	go func() {
		startedAt := time.Now()
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
			result.candidate, result.err = restoreExecution(stepCtx,
				definition, result.candidateState,
			)
		}
		if result.err == nil {
			result.stage = stepJobStageInvalid
		}
		result.finishedAt = time.Now()
		result.workDuration = result.finishedAt.Sub(startedAt)
		t.completions <- treeJobCompletion{
			processID: processID,
			attempt:   attempt,
			kind:      processJobStep,
			step:      result,
		}
	}()
}

func (t *treeRuntime) startRestore(process *processState) {
	attempt, ok := t.allocateAttempt(process)
	if !ok {
		return
	}
	processID := process.handle.processID
	definition := process.deployment.Definition()
	state := process.committedExecutionState
	restoreCtx, cancel := context.WithCancel(context.Background())
	t.setProcessJob(processID, &processJob{
		kind: processJobRestore, attempt: attempt, cancel: cancel,
	})
	go func() {
		execution, err := restoreExecution(restoreCtx, definition, state)
		t.completions <- treeJobCompletion{
			processID: processID, attempt: attempt, kind: processJobRestore,
			restore: restoreJobResult{execution: execution, err: err},
		}
	}()
}

func (t *treeRuntime) startPreparedEffect(process *processState, index int, record *preparedEffect) {
	if record.Phase == effectPhasePlanned {
		candidate := process.candidate()
		if err := candidate.prepared.Effects[index].begin(); err != nil {
			t.failProcessContract(process, failureCodeEngineEffectPhaseInvalid, err)
			return
		}
		if err := t.validateSnapshotCapacity(candidate); err != nil {
			t.failProcess(process, FailureKindExecution, failureCodeEngineLimitSnapshot, err)
			t.finishIfTerminal(process)
			return
		}
		process.adoptCandidate(candidate)
		record = &process.prepared.Effects[index]
		if record.Effect.Target() == EffectTargetDispatcher {
			if err := t.startPendingEffectCommit(process, uint32(index), *record); err != nil {
				t.failRuntime(err, process.handle.processID, EffectID{})
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
		observation := t.beginEffectAttempt(process, process.prepared.StepSequence, record.ID, EffectTargetFramework)
		operation, err := decodeFrameworkOperation(record.Effect.Payload())
		if err != nil {
			t.failProcessContract(process, failureCodeEngineFrameworkEffectSettlementInvalid, err)
			return
		}
		operation.dispatch(t, process, uint32(index), record, observation)
		return

	}
	t.startDispatch(process, uint32(index), *record, nil)
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
			result := failedChildStart(spec, FailureKindExecution, failureCodeEngineChildStartInterrupted,
				errors.New("child publication interrupted by parent termination"))
			err = record.settleChildStart(result)
		}
		if err != nil {
			t.failProcessContract(process, failureCodeEngineChildSettlementInvalid, err)
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
		t.startDispatch(process, batchIndex, *record, nil)
	case ReplayPolicyNever:
		if err := record.settleUnknown(); err != nil {
			t.failProcessContract(process, failureCodeEngineEffectRecoveryInvalid, err)
			return
		}

		settlement := *record.Settlement
		snapshot, err := t.captureTree()
		if err != nil {
			t.failRuntime(err, process.handle.processID, record.ID)
			return
		}
		boundary, err := newEffectBoundary(t.commitSequence+1,
			EffectBoundaryKindSettled,
			t.effectRequestFor(process, batchIndex, *record),
			settlement,
			t.head.Digest(),
			snapshot,
		)
		if err != nil {
			t.failRuntime(err, process.handle.processID, record.ID)
			return
		}
		t.startEffectCommit(&treeCommit{
			kind: treeCommitEffectSettled, processID: process.handle.processID,
			effectID: record.ID, snapshot: snapshot,
		}, boundary)
	default:
		t.failProcessContract(
			process, failureCodeEngineEffectRecoveryInvalid, errInvalidReplayPolicy,
		)
	}
}

func (t *treeRuntime) startChild(
	process *processState,
	record *preparedEffect,
	observation effectAttempt,
) {
	processID := process.handle.processID

	spec, err := decodeChildStartEffect(record.Effect.Payload())
	if err != nil {
		t.failProcessContract(process, failureCodeEngineFrameworkEffectSettlementInvalid, err)
		return
	}
	attempt, ok := t.allocateAttempt(process)
	if !ok {
		return
	}
	preparation := t.prepareChildStart(process, record.ID, spec)
	if preparation.plan == nil {
		if err := t.settleChildStart(process, record.ID, preparation.result, observation); err != nil {
			t.failProcessContract(process, failureCodeEngineChildSettlementInvalid, err)
			return
		}
		t.enqueueProcess(processID)
		return
	}
	startCtx, cancel := context.WithCancel(t.context)
	job := &processJob{
		kind: processJobChildStart, attempt: attempt, effectID: record.ID,
		childStart: preparation.plan, effectAttempt: observation, cancel: cancel,
	}
	t.setProcessJob(processID, job)
	go func() {
		result := preparation.plan.execute(startCtx)
		t.completions <- treeJobCompletion{
			processID:  processID,
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
	response chan processResponse,
) {
	processID := process.handle.processID
	dispatcher := process.deployment.dispatcher

	attempt, ok := t.allocateAttempt(process)
	if !ok {
		return
	}
	request := t.effectRequestFor(process, batchIndex, record)
	observation := t.beginEffectAttempt(process, process.prepared.StepSequence, record.ID, EffectTargetDispatcher)
	dispatchCtx, cancel := context.WithCancel(t.context)
	job := &processJob{
		kind:          processJobDispatch,
		attempt:       attempt,
		cancel:        cancel,
		effectID:      record.ID,
		effectAttempt: observation,
		response:      response,
	}
	t.setProcessJob(processID, job)
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
				processID, record.ID, t.incarnation, observation.id,
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
			dispatcher,
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
			var settlementErr error
			settlement, settlementErr = NewSettlement(record.ID, SettlementStatusUnknown, json.RawMessage(nullJSON))
			err = errors.Join(err, settlementErr)
		}
		t.completions <- treeJobCompletion{
			processID: processID,
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
	if process == nil && job != nil {
		panic("agent: owned work requires a tree member")
	}
	if job == nil || job.kind != completion.kind || job.attempt != completion.attempt {
		return
	}
	delete(t.jobs, completion.processID)
	t.inFlightWork.Add(-1)
	t.queueJoin(process)
	if job.cancel != nil {
		job.cancel()
	}
	if completion.kind == processJobStep {
		t.publishStepFinished(process, completion.step, job.stale || t.fault != nil)
	}
	if completion.kind == processJobDispatch {
		t.publishDispatchFinished(process, job, completion.dispatch)
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
		t.applyStepCompletion(process, completion.step)
	case processJobRestore:
		if completion.restore.err != nil {
			t.failProcess(process, failureKindForError(completion.restore.err, FailureKindExecution), failureCodeExecutionSnapshotUnrestorable, completion.restore.err)
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
	pending := &pendingChildStartPublication{
		parentID: parent.handle.processID, effectID: job.effectID,
		plan: plan, result: result, effectAttempt: job.effectAttempt,
	}
	transferred := false
	var publicationErr, checkpointErr error
	defer func() {
		if !transferred {
			t.discardChildStart(plan)
		}
		if publicationErr != nil {
			t.failProcessContract(parent, failureCodeEngineChildSettlementInvalid, publicationErr)
		} else if checkpointErr != nil {
			t.failRuntime(checkpointErr, parent.handle.processID, job.effectID)
		}
	}()
	if publicationErr = t.applyChildStart(pending); publicationErr != nil {
		return
	}

	pending.event = t.prepareSettlementEvent(parent,
		job.effectID, EffectTargetFramework,
		pending.childSettlementStatus(), job.effectAttempt, nil,
	)
	snapshot, err := t.captureTree()
	if err == nil {
		commit := &treeCommit{
			kind: treeCommitEffectSettled, processID: pending.parentID,
			effectID: pending.effectID, snapshot: snapshot, events: []eventFact{pending.event},
		}
		if pending.result.started() {
			commit.kind, commit.child, commit.events = treeCommitChildStart, pending, nil
		}
		err = t.startCheckpoint(commit, TreeCheckpointKindChildStart)
	}
	checkpointErr = err
	transferred = err == nil && pending.result.started()
}

func (t *treeRuntime) applyChildStart(pending *pendingChildStartPublication) error {
	if pending == nil || pending.plan == nil {
		return errors.New("child start publication is incomplete")
	}
	parent := t.processes[pending.parentID]
	if parent == nil {
		return errors.New("child start parent is missing")
	}
	if pending.result.started() {
		candidate := parent.candidate()
		if candidate.prepared == nil {
			return errors.New("child start parent has no prepared Step")
		}
		if err := candidate.commitProvisionalChildBudget(pending.plan.spec.Budget); err != nil {
			return err
		}
		handle := newProcessHandle(
			pending.plan.relation,
			pending.result.deployment.DeploymentRef(),
			pending.plan.spec.Budget,
			pending.plan.spec.Capabilities,
			pending.plan.treeLimits,
			pending.result.startedAt)
		handle.childRequestDigest = pending.plan.requestDigest
		child := newProcessState(
			handle, pending.result.deployment, pending.result.execution,
			pending.result.state, pending.result.startedAt, pending.plan.limits,
		)
		if _, err := t.applyChildStartSettlement(candidate, pending.effectID, pending.result.result); err != nil {
			return err
		}
		if err := t.validateSnapshotCapacity(candidate, child); err != nil {
			if !errors.Is(err, ErrResourceLimitExceeded) {
				return err
			}
			pending.result = childStartJobResult{result: failedChildStart(
				pending.plan.spec, FailureKindExecution, failureCodeEngineChildTreeLimit, err,
			)}
			_, err = t.applyChildStartSettlement(parent, pending.effectID, pending.result.result)
			return err
		}
		parent.adoptCandidate(candidate)
		t.addProcess(child)
		if parent.pendingControl.hasTerminalIntent() || parent.status.Terminal() {
			child.recordParentTermination(parent.effectiveTermination())
			t.stopProcessTree(child)
		}
		return nil
	}
	_, err := t.applyChildStartSettlement(parent, pending.effectID, pending.result.result)
	return err
}

func (t *treeRuntime) settleChildStart(
	parent *processState,
	effectID EffectID,
	result ChildStartResult,
	observation effectAttempt,
) error {
	status, err := t.applyChildStartSettlement(parent, effectID, result)
	if err != nil {
		return err
	}
	t.publishSettlementEvent(parent, effectID, EffectTargetFramework, status, observation, nil)
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

func (t *treeRuntime) failProcessContract(process *processState, code string, err error) {
	t.failProcess(process, FailureKindContract, code, err)
	t.finishIfTerminal(process)
}

func (t *treeRuntime) publishStepFinished(process *processState, result stepJobResult, discarded bool) {
	status := StepStatusSucceeded
	if result.err != nil {
		status = StepStatusFailed
	}
	if discarded {
		status = StepStatusDiscarded
	}
	work, delay := int64(result.workDuration), int64(time.Since(result.finishedAt))
	payload := marshalEventPayload(stepFinishedEventPayload{StepStatus: status, WorkDurationNS: &work, AdoptionDelayNS: &delay})
	t.publishEvent(process, EventStepFinished, EventPhaseAttempt, process.committedSteps+1, EffectID{}, payload)
}

func (t *treeRuntime) applyStepCompletion(
	process *processState,
	result stepJobResult,
) {
	sequence := process.committedSteps + 1
	if result.err != nil {
		code := failureCodeExecutionStepFailed
		switch result.stage {
		case stepJobStageSnapshot:
			code = failureCodeExecutionSnapshotFailed
		case stepJobStageRestore:
			code = failureCodeExecutionSnapshotUnrestorable
		}
		if sealed, ok := errors.AsType[*callbackError](result.err); ok && sealed != nil && result.stage == stepJobStageExecution && sealed.kind != FailureKindPanic && sealed.step != nil {
			failure := sealed.step.Failure
			if !failure.Valid() {
				t.failProcess(process, FailureKindContract, code, ErrInvalidFailure)
			} else {
				t.failProcess(process, failure.Kind(), failure.Code(), errors.New(failure.Message()))
			}
			return
		}
		t.failProcess(process, failureKindForError(result.err, FailureKindExecution), code, result.err)
		return
	}
	candidate, failure := process.prepareStep(result)
	if failure != nil {
		t.failProcess(process, failure.kind, failure.code, failure.cause)
		return
	}
	for _, record := range candidate.prepared.Effects {
		if record.Effect.Target() != EffectTargetFramework {
			continue
		}
		operation, err := decodeFrameworkOperation(record.Effect.Payload())
		if err == nil {
			if wait, ok := operation.(childWaitOperation); ok {
				err = wait.spec.validateRelations(process.handle.processID, t.processRelation)
			}
		}
		if err != nil {
			t.failProcessContract(process, failureCodeExecutionEffectInvalid, err)
			return
		}
	}
	if err := t.validateSnapshotCapacity(candidate); err != nil {
		t.failProcess(process, FailureKindExecution, failureCodeEngineLimitSnapshot, err)
		return
	}
	process.adoptCandidate(candidate)

	t.publishEvent(process, EventStepPrepared, EventPhaseAttempt, sequence, EffectID{}, emptyEventPayload())
}

// Attempt completion remains observable even when its candidate cannot be committed
// or a sibling has already stopped the runtime.
func (t *treeRuntime) publishDispatchFinished(process *processState, job *processJob, result dispatchJobResult) {
	if result.dropped > 0 {
		process.counters.DroppedDeltas = saturatingCountAdd(
			process.counters.DroppedDeltas,
			result.dropped,
		)

		payload := marshalEventPayload(deltaDroppedEventPayload{DroppedDeltaCount: result.dropped, AttemptID: job.effectAttempt.id})

		t.publishPreparedEvent(process, t.prepareEvent(process,
			EventDeltaDropped, EventPhaseAttempt,
			process.prepared.StepSequence, result.effectID, payload,
		))

	}
	t.publishSettlementEvent(process, result.effectID, EffectTargetDispatcher, result.settlement.Status(), job.effectAttempt, result.err)
}

func (t *treeRuntime) applyDispatchCompletion(
	process *processState,
	job *processJob,
	result dispatchJobResult,
) {
	index, record, nextErr := process.prepared.nextEffect()
	if nextErr != nil || record == nil || record.ID != result.effectID {
		return
	}
	settlement := result.settlement
	replaying := record.unknown()
	if !replaying {
		candidate := process.candidate()
		if err := candidate.prepared.Effects[index].settle(settlement, result.err); err != nil {
			t.failProcessContract(process, failureCodeEngineEffectSettlementInvalid, err)
			return
		}
		if err := t.validateSnapshotCapacity(candidate); err != nil {
			t.failRuntime(err, process.handle.processID, record.ID)
			return
		}
		process.adoptCandidate(candidate)
		record = &process.prepared.Effects[index]
	}
	if replaying {
		command := processCommand{settlement: settlement, response: job.response}
		if result.err != nil || settlement.Status() == SettlementStatusUnknown {
			command.reply(processResponse{err: errors.Join(ErrEffectOutcomeUnknown, result.err)})
			return
		}
		t.commitResolution(process, command)
		return
	}

	snapshot, err := t.captureTree()
	if err != nil {
		t.failRuntime(err, process.handle.processID, record.ID)
		return
	}
	request := t.effectRequestFor(process, uint32(index), *record)
	boundary, err := newEffectBoundary(t.commitSequence+1,
		EffectBoundaryKindSettled, request, settlement, t.head.Digest(), snapshot,
	)
	if err != nil {
		t.failRuntime(err, process.handle.processID, record.ID)
		return
	}
	commit := &treeCommit{
		kind: treeCommitEffectSettled, processID: process.handle.processID,
		effectID: record.ID, snapshot: snapshot,
	}
	t.startEffectCommit(commit, boundary)
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
			headDigest: t.head.Digest(), unresolvedEffectIDs: canonicalEffectIDs(unresolved), cause: t.fault,
		}
	}
	if !process.handle.finishJoin(joinErr) {
		return true
	}
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

func (t *treeRuntime) finishProcessBookkeeping(process *processState) {
	process.handle.finishBookkeeping()
	t.queueJoin(process)
}

func (t *treeRuntime) finishIfTerminal(process *processState) {
	if t.fault != nil || process == nil || !process.status.Terminal() {
		return
	}

	t.stageTerminal(process)
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
		if registration.spec.Boundary != boundary || !slices.Contains(registration.spec.Children, processID) {
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
			parent.recordFailure(FailureKindExecution, failureCodeEngineChildWaitSatisfactionEncodingFailed, err)
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
		// Registration proves membership; children stay retained until tree release.
		child := t.processes[childID]
		ready := child.status.Terminal()
		if registration.spec.Boundary == ChildWaitBoundaryDrained {
			ready = child.handle.joinDone() && child.handle.joinError() == nil
		}
		if ready {
			key, _ := child.handle.relation.ChildKey()
			outcome := ChildOutcome{key: key, result: child.result(), boundary: registration.spec.Boundary}
			if registration.spec.Boundary == ChildWaitBoundaryDrained {
				outcome.subtreeUnresolvedEffects = t.subtreeUnresolvedEffects(childID)
			}
			outcomes = append(outcomes, outcome)
		}
	}
	return outcomes, uint32(len(outcomes)) >= registration.spec.required()
}

func (t *treeRuntime) subtreeUnresolvedEffects(processID ProcessID) []UnresolvedEffect {
	return subtreeUnresolvedEffects(processID,
		func(id ProcessID) []ProcessID { return t.childrenByParent[id] },
		func(id ProcessID) Termination { return t.processes[id].termination })
}

func (t *treeRuntime) processRelation(id ProcessID) ProcessRelation {
	if process := t.processes[id]; process != nil {
		return process.handle.relation
	}
	return ProcessRelation{}
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
	if err := spec.validateRelations(parentID, t.processRelation); err != nil {
		return Signal{}, false, err
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
	if t.jobs[processID] != nil {
		panic("agent: cannot remove a Process with owned work")
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
	ctx = RequireContext(ctx)
	if err := ctx.Err(); err != nil {
		return nil, TreeSnapshot{}, err
	}
	if snapshot, stopped, err := t.captureStoppedTree(); stopped {
		return nil, snapshot, err
	}
	acquisition := &treeFreezeAcquisition{
		response: make(chan treeFreezeAcquisitionResult, 1),
		canceled: make(chan struct{}),
	}
	select {
	case t.freezeCommands <- treeCommand{
		kind: treeCommandAcquireFreeze, acquisition: acquisition,
	}:
	case <-t.done:
		snapshot, _, err := t.captureStoppedTree()
		return nil, snapshot, err
	case <-ctx.Done():
		return nil, TreeSnapshot{}, ctx.Err()
	}
	select {
	case result := <-acquisition.response:
		return result.freeze, result.snapshot, result.err
	case <-t.done:
		snapshot, _, err := t.captureStoppedTree()
		return nil, snapshot, err
	case <-ctx.Done():
		close(acquisition.canceled)
		return nil, TreeSnapshot{}, ctx.Err()
	}
}

func (t *treeRuntime) captureStoppedTree() (TreeSnapshot, bool, error) {
	select {
	case <-t.done:
		if t.fault != nil {
			return TreeSnapshot{}, true, t.fault
		}
		return t.head, true, nil
	default:
		return TreeSnapshot{}, false, nil
	}
}

// The tree owner installs wait registrations around one candidate adoption.
// A rejected local transition cannot leave a live registration behind.
func (t *treeRuntime) finalizePrepared(process *processState) error {
	finalization, err := newPreparedStepFinalization(process, process.prepared)
	if err != nil {
		return err
	}
	if err := finalization.prepareSettlements(); err != nil {
		return err
	}
	var registered []WaitID
	var immediate []Signal
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
			immediate = append(immediate, signal)
		}
	}
	if err := finalization.prepareTransition(time.Now().Round(0).UTC()); err != nil {
		return err
	}
	candidate := process.candidate()
	candidate.adopt(finalization)
	if len(immediate) != 0 {
		admitted, admissionErr := candidate.prepareSignals(immediate, signalSourceChildWait)
		if admissionErr != nil {
			return admissionErr
		}
		candidate = admitted
	}
	if err := t.validateSnapshotCapacity(candidate); err != nil {
		return err
	}
	process.adoptCandidate(candidate)
	adopted = true
	for _, waitID := range finalization.consumedChildWaits {
		t.unregisterChildWait(process.handle.processID, waitID)
	}
	for _, waitID := range finalization.commit.closedChildWaits {
		t.unregisterChildWait(process.handle.processID, waitID)
	}

	payload := marshalEventPayload(stepCommittedEventPayload{ProcessStatus: process.status})
	t.stageEvent(process, EventStepCommitted, EventPhaseCommitted, process.committedSteps, EffectID{}, payload)
	if process.status == StatusPaused {
		t.stageEvent(process, EventProcessPaused, EventPhaseCommitted, 0, EffectID{}, emptyEventPayload())
	}
	return nil
}

func (t *treeRuntime) terminatePreparedProcess(process *processState) {
	if t.jobs[process.handle.processID] != nil {
		t.stopProcessTree(process)
		return
	}
	for index := range process.prepared.Effects {
		record := &process.prepared.Effects[index]
		if record.Phase != effectPhasePending {
			continue
		}
		if process.restoredPending.matches(record.ID) {
			// Recovery owns the next completion. Termination resumes here after it
			// settles, retaining any permissions already revoked in this pass.
			t.recoverPendingEffect(process, uint32(index), record)
			return
		}
		// Completed attempts install their settlement before termination. Without
		// a job or recovered uncertainty, this incarnation owns unused permission.
		if err := record.revokeDispatch(); err != nil {
			process.recordFailure(FailureKindContract, failureCodeEngineEffectPhaseInvalid, err)
			break
		}
	}
	process.preparedExecution = nil
	process.execution = nil
	t.installTerminationWithUnresolved(process, stepOutcome{}, process.unknownEffectIDs())
}

func (t *treeRuntime) failProcess(process *processState, kind FailureKind, code string, err error) {
	process.recordFailure(kind, code, err)
	if process.prepared != nil {
		t.terminatePreparedProcess(process)
	} else {
		t.installTermination(process, stepOutcome{})
	}
}

func (t *treeRuntime) installTermination(process *processState, outcome stepOutcome) {
	t.installTerminationWithUnresolved(process, outcome, nil)
}

func (t *treeRuntime) installTerminationWithUnresolved(process *processState, outcome stepOutcome, unresolvedEffectIDs []EffectID) {
	termination := process.resolveStepTermination(outcome)
	process.installTermination(termination.withUnresolvedEffectIDs(unresolvedEffectIDs), Payload{}, time.Now().Round(0).UTC())
	for _, waitID := range process.mailbox.closeAllWaits() {
		t.unregisterChildWait(process.handle.processID, waitID)
	}
}

func emptyEventPayload() json.RawMessage { return json.RawMessage("{}") }

func (t *treeRuntime) admitSignals(process *processState, signals []Signal, source signalSource) ([]eventFact, error) {
	candidate, err := process.prepareSignals(signals, source)
	if err != nil || candidate == nil {
		return nil, err
	}
	if err := t.validateSnapshotCapacity(candidate); err != nil {
		return nil, err
	}
	records := candidate.mailbox.records[len(process.mailbox.records):]
	process.adoptCandidate(candidate)
	return t.prepareSignalEvents(process, records), nil
}

func (t *treeRuntime) validateSnapshotCapacity(candidates ...*processState) error {
	if !t.processes[t.rootID].treeLimits.MaxSnapshotBytes.limited {
		if len(candidates) == 0 {
			// Start and Restore admit every member before publishing the tree.
			for _, member := range t.processes {
				if _, err := member.snapshotAdmissionSize(); err != nil {
					return err
				}
			}
			return nil
		}
		// Existing members already passed their own admission; only candidates can
		// change a Process quota when the tree has no aggregate quota.
		for _, candidate := range candidates {
			if _, err := candidate.snapshotAdmissionSize(); err != nil {
				return err
			}
		}
		return nil
	}

	header, err := json.Marshal(t.treeSnapshotBase())
	if err != nil {
		return err
	}
	// The header contains an empty JSON array. Adding raw object encodings and
	// separators also reserves known wait settlements without validating a half-installed
	// child-control or child-wait transition.
	size := uint64(len(header))
	index := 0
	members := maps.Clone(t.processes)
	for _, candidate := range candidates {
		members[candidate.handle.processID] = candidate
	}
	for _, member := range members {
		memberSize, err := member.snapshotAdmissionSize()
		if err != nil {
			return err
		}
		var separator uint64
		if index > 0 {
			separator = 1
		}
		if !resourceQuantitiesFit(^uint64(0), size, memberSize, separator) {
			return ErrCounterExhausted
		}
		size += memberSize + separator
		if !t.processes[t.rootID].treeLimits.MaxSnapshotBytes.Allows(size) {
			return ErrResourceLimitExceeded
		}
		index++
	}
	return nil
}

func (t *treeRuntime) settleFramework(process *processState, record *preparedEffect, observation effectAttempt) {
	if err := record.settleFramework(); err != nil {
		t.failProcessContract(process, failureCodeEngineFrameworkEffectSettlementInvalid, err)
		return
	}
	t.publishSettlementEvent(process, record.ID, EffectTargetFramework, record.Settlement.Status(), observation, nil)
	t.enqueueProcess(process.handle.processID)
}
