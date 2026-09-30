package agent

import (
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"math"
	"slices"
	"time"
)

// treeRuntime serializes authoritative changes because sibling jobs must not
// publish incompatible tree cuts. Fenced completions let computation and dispatch
// run concurrently without sharing commit authority.
type treeRuntime struct {
	engine     *Engine
	rootID     ProcessID
	treeLimits TreeLimits
	writer     *headWriter
	events     eventRecorder

	// External readers need scheduling liveness without acquiring execution
	// state. Atomics expose that view while commands and completions preserve
	// one mutation owner.
	context         context.Context
	processCommands chan processTreeCommand
	freezeCommands  chan freezeCommand
	completions     chan treeJobCompletion
	inspections     chan chan TreeInspection

	// Everything below is owner-line state. Keeping it lock-free makes commit,
	// scheduling, freeze, and checkpoint order a single explicit state machine.
	members         treeMembers
	childWaits      childWaitRegistry
	joinCandidates  map[ProcessID]*processState
	runQueue        runQueue
	jobs            *jobTable
	fault           error
	publications    publicationLedger
	freeze          freezeBarrier
	done            chan struct{}
	finalInspection TreeInspection
}

// processTreeCommand routes one Process control request to its tree owner.
type processTreeCommand struct {
	processID ProcessID
	request   processRequest
	reply     processReply
}

// freezeCommand is sealed: only acquisition and release of the snapshot
// barrier travel on the freeze lane.
type freezeCommand interface{ freezeCommand() }

type acquireFreezeCommand struct{ acquisition *treeFreezeAcquisition }

func (acquireFreezeCommand) freezeCommand() {}

type releaseFreezeCommand struct {
	freeze   *treeFreeze
	response chan error
}

func (releaseFreezeCommand) freezeCommand() {}

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
	reply     processReply
	events    []eventFact
	child     *pendingChildStartPublication
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

func (stepJobResult) jobKind() processJobKind { return processJobStep }

type restoreJobResult struct {
	execution Execution
	err       error
}

func (restoreJobResult) jobKind() processJobKind { return processJobRestore }

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

func (dispatchJobResult) jobKind() processJobKind { return processJobDispatch }

func newTreeRuntime(
	engine *Engine,
	rootID ProcessID,
	treeLimits TreeLimits,
	ctx context.Context,
	processes ...*processState,
) *treeRuntime {
	writer := newHeadWriter(engine.committer)
	detached := context.WithoutCancel(RequireContext(ctx))
	runtime := &treeRuntime{
		engine:          engine,
		rootID:          rootID,
		treeLimits:      treeLimits,
		writer:          writer,
		events:          eventRecorder{observation: engine.observation, writer: writer, context: detached},
		context:         detached,
		processCommands: make(chan processTreeCommand, treeCommandBufferCapacity),
		freezeCommands:  make(chan freezeCommand, treeCommandBufferCapacity),
		completions:     make(chan treeJobCompletion),
		inspections:     make(chan chan TreeInspection, treeCommandBufferCapacity),
		members:         newTreeMembers(len(processes)),
		childWaits:      childWaitRegistry{},
		joinCandidates:  make(map[ProcessID]*processState),
		runQueue:        newRunQueue(len(processes)),
		jobs:            newJobTable(len(processes)),
		publications:    publicationLedger{},
		done:            make(chan struct{}),
	}
	for _, process := range processes {
		runtime.addProcess(process)
	}
	return runtime
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
	t.finalInspection = t.buildInspection()
	t.finalInspection.Stopped = true
	close(t.done)
}

func (t *treeRuntime) publishInitialProcessEvents() {
	for _, process := range t.members.ordered() {
		if process.status.Terminal() {
			continue
		}
		if process.restored {
			t.events.emit(process, EventProcessRestored, EventPhaseCommitted, 0, EffectID{}, emptyEventPayload())
		} else {
			t.events.emit(process, EventProcessStarted, EventPhaseCommitted, 0, EffectID{}, emptyEventPayload())
		}
	}
}

func (t *treeRuntime) watchHostTermination(rootContext context.Context) func() bool {
	return context.AfterFunc(rootContext, func() {
		select {
		case t.processCommands <- processTreeCommand{
			processID: t.rootID,
			request:   hostTerminationRequest{cause: rootContext.Err()},
		}:
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
	commitDone := t.writer.done
	freezeCommands := t.freezeCommands
	processCommands := t.processCommands
	completions := t.completions
	freezeCancellation := t.freeze.cancellation()
	if t.mutationsBlocked() {
		processCommands = nil
		completions = nil
	}
	if t.writer.committing() {
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
			t.applyFreezeCommand(command)
		case response := <-t.inspections:
			t.replyInspection(response)
			continue
		case command := <-processCommands:
			t.routeProcessCommand(command)
		case completion := <-completions:
			t.applyCompletion(completion)
		case <-freezeCancellation:
			t.releaseCurrentFreeze()
		}
		return
	}
}

func (t *treeRuntime) tryCommitCompletion() bool {
	if !t.writer.committing() {
		return false
	}
	select {
	case completion := <-t.writer.done:
		t.applyTreeCommitCompletion(completion)
		return true
	default:
		return false
	}
}

func (t *treeRuntime) tryFreezeCancellation() bool {
	canceled := t.freeze.cancellation()
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
	if t.writer.committing() {
		return false
	}
	select {
	case command := <-t.freezeCommands:
		t.applyFreezeCommand(command)
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
		t.routeProcessCommand(command)
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
	return t.writer.committing() || t.freeze.held()
}

func (t *treeRuntime) enqueueProcess(processID ProcessID) {
	process := t.members.get(processID)
	if t.fault != nil || process == nil || process.status.Terminal() || t.jobs.get(processID) != nil {
		return
	}
	t.runQueue.push(processID)
}

func (t *treeRuntime) dequeueProcess() *processState {
	for {
		processID, queued := t.runQueue.pop()
		if !queued {
			return nil
		}
		process := t.members.get(processID)
		if process != nil && !process.status.Terminal() && t.jobs.get(processID) == nil {
			return process
		}
	}
}

func (t *treeRuntime) advanceOne() bool {
	if t.writer.committing() || t.fault != nil || t.freeze.engaged() {
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
	if process.pendingControl.pause.valid() && process.prepared.Intent.Kind() == TransitionKindWait {
		process.discardPreparedStep()
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
	if failure := t.finalizePrepared(process); failure != nil {
		process.recordFailure(failure.kind, failure.code, failure.cause)
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
	if t.members.get(processID) == nil {
		panic("agent: owned work requires a tree member")
	}
	t.jobs.start(processID, job)
}

// ownsActiveWork is the lock-free view Engine.Close uses: jobs, the in-flight
// commit, and a held freeze each keep the tree busy.
func (t *treeRuntime) ownsActiveWork() bool {
	return t.jobs.active.Load() != 0 || t.writer.busy.Load() || t.freeze.engagedFlag.Load()
}

func (t *treeRuntime) canStop() bool {
	if t.freeze.engaged() || t.writer.committing() || !t.jobs.empty() ||
		len(t.publications) != 0 {
		return false
	}
	for _, process := range t.members.all() {
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
	if !process.handle.capabilities.Allows(spec.Capabilities) {
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
	if reserveProcessStartErr := t.engine.reserveProcessStart(
		relation, spec.DeploymentRef, requestDigest,
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
		requestDigest: requestDigest,
	}}
}

// Membership and in-flight child jobs are the resource facts. A completed job
// installs its child before scheduling resumes; durable publication blocks that
// scheduling lane until acknowledgment. No parallel reservation counter exists.
func (t *treeRuntime) canStartChild(parent *processState) bool {
	limits := t.treeLimits
	if !limits.admitsDepth(parent.handle.relation.Depth() + 1) {
		return false
	}
	children := t.members.childrenOf(parent.handle.processID)
	childCount, treeCount := uint64(len(children)), uint64(t.members.len())
	var active uint64
	for _, childID := range children {
		if !t.members.get(childID).status.Terminal() {
			active++
		}
	}
	for _, job := range t.jobs.all() {
		if job.childStart == nil || t.members.get(job.childStart.childID) != nil {
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
func (t *treeRuntime) controlChild(
	parent *processState,
	index uint32,
	record *preparedEffect,
	request childControlEffectWire,
	observation effectAttempt,
) {
	result := request.result()
	child := t.members.get(request.ChildID)
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
	t.events.publishSettlement(parent, record.ID, EffectTargetFramework, record.Settlement.Status(), observation, nil)
	t.enqueueProcess(parent.handle.processID)
	t.commitSettledEffect(parent, index, *record)
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
		t.writer.incarnation(),
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
	return t.commitEffect(process, treeCommitEffectPending, EffectBoundaryKindPending, batchIndex, record, Settlement{}, nil)
}

// commitSettledEffect makes record's adopted settlement durable. A settlement
// that cannot be committed stops the writer with record left unresolved.
func (t *treeRuntime) commitSettledEffect(process *processState, batchIndex uint32, record preparedEffect) {
	err := t.commitEffect(process, treeCommitEffectSettled, EffectBoundaryKindSettled, batchIndex, record, *record.Settlement, nil)
	if err != nil {
		t.failRuntime(err, process.handle.processID, record.ID)
	}
}

// commitEffect captures the prospective tree and starts the Effect boundary
// of kind for record; reply, when present, answers the requesting caller.
func (t *treeRuntime) commitEffect(
	process *processState,
	commitKind treeCommitKind,
	boundaryKind EffectBoundaryKind,
	batchIndex uint32,
	record preparedEffect,
	settlement Settlement,
	reply processReply,
) error {
	snapshot, err := t.captureTree()
	if err != nil {
		return err
	}
	commit := &treeCommit{
		kind: commitKind, processID: process.handle.processID,
		effectID: record.ID, snapshot: snapshot, reply: reply,
	}
	return t.writer.commitEffect(t.context, commit, boundaryKind, t.effectRequestFor(process, batchIndex, record), settlement)
}

func (t *treeRuntime) startUnknownResolutionCommit(
	process *processState,
	index int,
	settlement Settlement,
	reply processReply,
) error {
	return t.commitEffect(process, treeCommitEffectResolved, EffectBoundaryKindResolved,
		uint32(index), process.prepared.Effects[index], settlement, reply)
}

func (t *treeRuntime) startCheckpointCommit(
	kind TreeCheckpointKind,
	snapshot TreeSnapshot,
) error {
	return t.startCheckpoint(&treeCommit{kind: treeCommitCheckpoint, snapshot: snapshot}, kind)
}

func (t *treeRuntime) startSignalCommit(process *processState, reply processReply, events []eventFact) error {
	snapshot, err := t.captureTree()
	if err != nil {
		return err
	}
	return t.startCheckpoint(&treeCommit{
		kind: treeCommitSignals, processID: process.handle.processID,
		snapshot: snapshot, reply: reply, events: events,
	}, TreeCheckpointKindSignals)
}

func (t *treeRuntime) startCheckpoint(commit *treeCommit, kind TreeCheckpointKind) error {
	return t.writer.commitCheckpoint(t.context, commit, kind)
}

func (t *treeRuntime) applyTreeCommitCompletion(completion treeCommitCompletion) {
	commit, current := t.writer.settle(completion)
	if !current {
		return
	}
	if completion.err != nil {
		t.applyFailedTreeCommit(commit, completion.err)
		return
	}
	t.applySuccessfulTreeCommit(commit)
}

func (t *treeRuntime) applyFailedTreeCommit(commit *treeCommit, commitErr error) {
	commit.reply.send(processResponse{err: commitErr})
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
	t.publishAcknowledgedChanges()
	process := t.members.get(commit.processID)
	for _, event := range commit.events {
		t.events.publish(process, event)
	}
	switch commit.kind {
	case treeCommitEffectPending, treeCommitEffectSettled:
		t.enqueueProcess(commit.processID)
	case treeCommitEffectResolved:
		commit.reply.send(processResponse{})
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
		commit.reply.send(processResponse{accepted: true})
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
	parent := t.members.get(parentID)
	if child := t.members.get(plan.childID); child != nil {
		if parent != nil {
			parent.releaseCommittedChildBudget(plan.spec.Budget)
		}
		t.removeProcess(plan.childID)
	} else if parent != nil {
		parent.releaseProvisionalChildBudget(plan.spec.Budget)
	}
	t.runQueue.remove(plan.childID)
	delete(t.publications, plan.childID)
	t.engine.discardProcessStart(plan.childID)
}

func (t *treeRuntime) publishChildStart(pending *pendingChildStartPublication) error {
	if pending == nil || pending.plan == nil {
		return errors.New("child start publication is incomplete")
	}
	if pending.result.started() {
		child := t.members.get(pending.plan.childID)
		if child == nil {
			return errors.New("started child is missing from prospective tree")
		}
		t.engine.publishProcessStart(child.handle)
		t.events.emit(child, EventProcessStarted, EventPhaseCommitted, 0, EffectID{}, emptyEventPayload())
	}
	parent := t.members.get(pending.parentID)
	if pending.event.processID.Valid() {
		t.events.publish(parent, pending.event)
	} else {
		t.events.publishSettlement(parent, pending.effectID, EffectTargetFramework,
			pending.childSettlementStatus(), pending.effectAttempt, nil,
		)
	}
	return nil
}

func (t *treeRuntime) tryStartCheckpoint() bool {
	if t.fault != nil || t.writer.committing() || t.freeze.engaged() || !t.readyForCheckpoint() {
		return false
	}
	kind := t.checkpointKind()
	snapshot, err := t.captureTree()
	if err != nil {
		t.failRuntime(err, ProcessID{}, EffectID{})
		return true
	}
	if snapshot.Digest() == t.writer.head().Digest() {
		// Control changes can return to the acknowledged state without changing
		// its recovery cut. Publishing those facts must not require another write.
		published := len(t.publications) != 0
		t.publishAcknowledgedChanges()
		return published
	}
	if err := t.startCheckpointCommit(kind, snapshot); err != nil {
		t.failRuntime(err, ProcessID{}, EffectID{})
	}
	return true
}

// An idle tree checkpoints at once. Busy trees still publish a staged fact of
// a Process that will not advance on its own: terminal, waiting, or paused.
func (t *treeRuntime) readyForCheckpoint() bool {
	if t.jobs.empty() && t.runQueue.empty() {
		return true
	}
	for processID := range t.publications {
		status := t.members.get(processID).status
		if status.Terminal() || status == StatusWaiting || status == StatusPaused {
			return true
		}
	}
	return false
}

func (t *treeRuntime) checkpointKind() TreeCheckpointKind {
	return classifyCheckpointCut(func(yield func(Status, *preparedStep) bool) {
		for _, process := range t.members.all() {
			if !yield(process.status, process.prepared) {
				return
			}
		}
	})
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
	if t.publications.owesTerminal(process.handle.processID) {
		return
	}
	event := t.events.prepare(process,
		EventProcessFinished, EventPhaseCommitted, 0, EffectID{}, process.terminalEventPayload(),
	)
	t.propagateProcessTermination(process)
	t.publications.stageTerminal(event)
}

func (t *treeRuntime) stageCommittedEvent(event eventFact) {
	if event.phase != EventPhaseCommitted || event.relation.RootID() != t.rootID ||
		t.members.get(event.processID) == nil {
		panic("agent: invalid committed Event")
	}
	t.publications.stage(event)
}

// The acknowledged head supplies publication order and outcomes;
// publications owns what is still owed. Every staged fact belongs to a
// Process the same cut captured, so draining the head must leave nothing owed.
// A leftover entry would silently make scheduling ready forever, so it stops the
// writer instead.
func (t *treeRuntime) publishAcknowledgedChanges() {
	for _, snapshot := range t.writer.head().state.ProcessSnapshots {
		processID := snapshot.ProcessID()
		publication, pending := t.publications.take(processID)
		if !pending {
			continue
		}
		process := t.members.get(processID)
		for _, event := range publication.events {
			t.events.publish(process, event)
		}
		if !publication.terminal {
			continue
		}
		result, terminal := snapshot.Result()
		if !terminal {
			panic("agent: terminal publication requires an acknowledged outcome")
		}
		process.handle.publishResult(result)
		t.finishProcessBookkeeping(process)
	}
	if len(t.publications) != 0 {
		panic("agent: staged facts outlived the acknowledged tree cut")
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
	if !t.writer.head().Valid() {
		panic("agent: committer failure requires an acknowledged tree")
	}
	t.fault = cause
	unresolvedByProcess := t.unresolvedEffectsAtFailure(processID, effectID)
	t.abandonJobs()
	clear(t.publications)
	t.runQueue.clear()
	acknowledged := make(map[ProcessID]struct{}, len(t.writer.head().state.ProcessSnapshots))
	for _, snapshot := range t.writer.head().state.ProcessSnapshots {
		acknowledged[snapshot.ProcessID()] = struct{}{}
	}
	for _, process := range t.members.ordered() {
		memberID := process.handle.processID
		if _, published := acknowledged[memberID]; !published {
			// A prospective child that never entered an acknowledged head has
			// no published lifecycle to stop.
			t.engine.discardProcessStart(memberID)
			t.removeProcess(memberID)
			continue
		}
		t.stopProcessRuntime(process, cause, unresolvedByProcess[memberID])
	}
	if t.freeze.engaged() {
		// An answered acquisition needs no second reply; releaseFreeze reports
		// the fault to its holder.
		t.releaseCurrentFreeze().answer(treeFreezeAcquisitionResult{err: cause})
	}
}

// A started dispatch or child start may have external effects this instance
// can no longer adopt, so its identity joins the retained Unknown settlements.
func (t *treeRuntime) unresolvedEffectsAtFailure(processID ProcessID, effectID EffectID) map[ProcessID][]EffectID {
	unresolved := make(map[ProcessID][]EffectID, t.members.len())
	for candidateID, process := range t.members.all() {
		unresolved[candidateID] = process.unknownEffectIDs()
	}
	if processID.Valid() && effectID.Valid() {
		unresolved[processID] = append(unresolved[processID], effectID)
	}
	for candidateID, job := range t.jobs.all() {
		if effectID, uncertain := job.uncertainEffect(); uncertain {
			unresolved[candidateID] = append(unresolved[candidateID], effectID)
		}
	}
	return unresolved
}

func (t *treeRuntime) abandonJobs() {
	for _, job := range t.jobs.all() {
		job.abandon()
		if job.kind == processJobChildStart {
			t.abandonChildStartJob(job)
		}
	}
}

func (t *treeRuntime) stopProcessRuntime(process *processState, cause error, unresolved []EffectID) {
	processID := process.handle.processID
	if !process.handle.publishRuntimeFailure(&RuntimeError{
		processID: processID, incarnationID: t.writer.incarnation(), headDigest: t.writer.head().Digest(),
		unresolvedEffectIDs: canonicalEffectIDs(unresolved), cause: cause,
	}) {
		return
	}
	failure := newTreeRuntimeFailure(cause)
	payload := marshalEventPayload(runtimeStoppedEventPayload{
		FailureKind: failure.Kind(), FailureCode: failure.Code(),
	})
	t.events.emit(process, EventRuntimeStopped, EventPhaseAttempt, 0, EffectID{}, payload)
	t.finishProcessBookkeeping(process)
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

func (t *treeRuntime) applyFreezeCommand(command freezeCommand) {
	switch command := command.(type) {
	case acquireFreezeCommand:
		t.acquireFreeze(command.acquisition)
	case releaseFreezeCommand:
		command.response <- t.releaseFreeze(command.freeze)
	}
}

func (t *treeRuntime) routeProcessCommand(command processTreeCommand) {
	process := t.members.get(command.processID)
	if process == nil {
		command.reply.send(processResponse{err: fmt.Errorf("%w: Process %q is not owned by this tree", ErrInvalidProcessControl, command.processID)})
		return
	}
	t.applyProcessCommand(process, command.request, command.reply)
}

func (t *treeRuntime) applyProcessCommand(process *processState, request processRequest, reply processReply) {
	if t.fault != nil {
		reply.send(processResponse{err: process.handle.closedRequestError()})
		return
	}
	if termination, ok := request.(hostTerminationRequest); ok {
		if !process.status.Terminal() {
			process.recordHostTermination(termination.cause)
			t.stopProcessTree(process)
		}
		return
	}
	if process.status.Terminal() {
		reply.send(processResponse{err: ErrProcessFinished})
		return
	}
	switch request := request.(type) {
	case deliverSignalsRequest:
		t.deliverSignals(process, request.requests, reply)
	case pauseRequest:
		reply.send(processResponse{err: process.requestPause(request.reason)})
	case resumeRequest:
		err := process.resume()
		if err == nil {
			t.stageEvent(process, EventProcessResumed, EventPhaseCommitted, 0, EffectID{}, emptyEventPayload())
		}
		reply.send(processResponse{err: err})
	case cancelRequest:
		process.requestCancellation(request.intent)
	case killRequest:
		reply.send(processResponse{err: process.requestKill(request.reason)})
	case resolveUnknownEffectRequest:
		t.resolveUnknownEffect(process, request.settlement, reply)
		return
	case replayUnknownEffectRequest:
		t.replayUnknownEffect(process, request.effectID, reply)
		return
	default:
		panic("agent: unhandled Process request")
	}
	t.scheduleControl(process)
}

// A recorded control intent stops or pauses owned work before the Process is
// scheduled again.
func (t *treeRuntime) scheduleControl(process *processState) {
	if process.pendingControl.hasTerminalIntent() {
		t.stopProcessTree(process)
	} else if process.pendingControl.pause.valid() {
		t.invalidateStep(process)
	}
	t.finishIfTerminal(process)
	if !process.status.Terminal() {
		t.enqueueProcess(process.handle.processID)
	}
}

func (t *treeRuntime) resolveUnknownEffect(process *processState, settlement Settlement, reply processReply) {
	if process.status.Terminal() || process.pendingControl.hasTerminalIntent() {
		reply.send(processResponse{err: ErrProcessFinished})
		return
	}
	if t.jobs.get(process.handle.processID) != nil {
		reply.send(processResponse{err: ErrEffectNotPending})
		return
	}
	t.commitResolution(process, settlement, reply)
}

func (t *treeRuntime) commitResolution(process *processState, settlement Settlement, reply processReply) {
	candidate, index, err := process.prepareResolution(settlement, t.treeLimits)
	if err == nil {
		err = t.validateSnapshotCapacity(candidate)
	}
	if err != nil {
		reply.send(processResponse{err: err})
		return
	}
	process.adoptCandidate(candidate)
	record := process.prepared.Effects[index]
	payload := marshalEventPayload(effectResolvedEventPayload{
		EffectTarget: record.Effect.Target(), SettlementStatus: settlement.Status(),
	})
	t.stageEvent(process, EventEffectResolved, EventPhaseCommitted,
		process.prepared.StepSequence, record.ID, payload)

	if err := t.startUnknownResolutionCommit(process, index, settlement, reply); err != nil {
		reply.send(processResponse{err: err})
		t.failRuntime(err, process.handle.processID, settlement.EffectID())
	}
}

func (t *treeRuntime) replayUnknownEffect(process *processState, effectID EffectID, reply processReply) {
	if process.pendingControl.hasTerminalIntent() {
		reply.send(processResponse{err: ErrProcessFinished})
		return
	}
	if process.prepared == nil || t.jobs.get(process.handle.processID) != nil {
		reply.send(processResponse{err: ErrEffectNotPending})
		return
	}
	index, record, err := process.prepared.nextEffect()
	if err != nil || record == nil || record.ID != effectID || !record.unknown() {
		reply.send(processResponse{err: ErrEffectNotPending})
		return
	}
	policy, err := dispatcherReplayPolicy(process.deployment.dispatcher, record.Effect)
	if err != nil || record.Effect.Target() != EffectTargetDispatcher || policy != ReplayPolicySameIdentity {
		reply.send(processResponse{err: errors.Join(ErrEffectReplayForbidden, err)})
		return
	}
	// The committed Unknown already retains the exact uncertain operation.
	// Keep it intact while the same logical operation is reconciled: revoking
	// an unused new attempt must never erase evidence of an earlier attempt.
	t.startDispatch(process, uint32(index), *record, reply)
}

func (t *treeRuntime) acquireFreeze(acquisition *treeFreezeAcquisition) {
	if acquisition == nil || acquisition.response == nil || acquisition.canceled == nil || t.freeze.engaged() {
		if acquisition != nil && acquisition.response != nil {
			acquisition.response <- treeFreezeAcquisitionResult{err: ErrEngineQuiescenceUnavailable}
		}
		return
	}
	if t.fault != nil {
		acquisition.response <- treeFreezeAcquisitionResult{err: t.fault}
		return
	}
	t.freeze.begin(acquisition, &treeFreeze{runtime: t})
	t.completeFreeze()
}

func (t *treeRuntime) completeFreeze() {
	if !t.freeze.engaged() || t.freeze.held() || t.writer.committing() || t.freezeBlockedByJob() {
		return
	}
	snapshot, err := t.captureTree()
	if err != nil {
		t.releaseCurrentFreeze().answer(treeFreezeAcquisitionResult{err: err})
		return
	}
	if snapshot.Digest() != t.writer.head().Digest() {
		if err := t.startCheckpointCommit(t.checkpointKind(), snapshot); err != nil {
			t.failRuntime(err, ProcessID{}, EffectID{})
		}
		return
	}
	t.freeze.grant(snapshot)
}

// Computation can be discarded under a freeze, but external work must settle;
// once every Process is terminal, no job may remain.
func (t *treeRuntime) freezeBlockedByJob() bool {
	if !t.freeze.engaged() {
		return false
	}
	if t.members.allTerminal() {
		return !t.jobs.empty()
	}
	return t.jobs.hasExternal()
}

func (t *treeRuntime) captureTree() (TreeSnapshot, error) {
	wire := t.treeSnapshotBase()
	for _, process := range t.members.all() {
		snapshot, err := process.capture()
		if err != nil {
			return TreeSnapshot{}, err
		}
		wire.ProcessSnapshots = append(wire.ProcessSnapshots, snapshot)
	}
	return treeSnapshotFromWire(wire)
}

func (t *treeRuntime) treeSnapshotBase() treeSnapshotWire {
	wire := treeSnapshotWire{RootID: t.rootID, TreeLimits: t.treeLimits, ProcessSnapshots: []ProcessSnapshot{}}
	wire.IncarnationID = t.writer.incarnation()
	wire.ChildWaits = t.childWaits.wire()
	return wire
}

func (t *treeRuntime) releaseFreeze(freeze *treeFreeze) error {
	if !t.freeze.owns(freeze) {
		if t.fault != nil {
			// A runtime failure already released this barrier. Its holder needs
			// the cause, not the absence it produced.
			return t.fault
		}
		return ErrEngineQuiescenceUnavailable
	}
	t.releaseCurrentFreeze()
	return nil
}

// releaseCurrentFreeze is used only by the tree owner after it has selected
// the active freeze. External capabilities still pass through releaseFreeze so
// stale or foreign authority is rejected rather than silently accepted.
func (t *treeRuntime) releaseCurrentFreeze() *activeTreeFreeze {
	ended := t.freeze.end()
	for _, process := range t.members.all() {
		if !process.status.Terminal() {
			t.enqueueProcess(process.handle.processID)
		}
	}
	return ended
}

func (t *treeRuntime) invalidateStep(process *processState) {
	job := t.jobs.get(process.handle.processID)
	if job == nil || job.kind != processJobStep || job.stale {
		return
	}
	job.interrupt()
}

// Stopping owned work cannot wait for an ancestor's external call to return.
// Terminal intermediate Processes still own descendants that may be draining.
func (t *treeRuntime) stopProcessTree(process *processState) {
	termination := process.effectiveTermination()
	if !process.status.Terminal() {
		if job := t.jobs.get(process.handle.processID); job != nil {
			job.interrupt()
		}
		t.enqueueProcess(process.handle.processID)
	}
	for _, childID := range t.members.childrenOf(process.handle.processID) {
		child := t.members.get(childID)
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

func (t *treeRuntime) deliverSignals(process *processState, requests []SignalRequest, reply processReply) {
	signals := make([]Signal, 0, len(requests))
	for _, request := range requests {
		signal, err := request.signal()
		if err != nil {
			reply.send(processResponse{err: err})
			return
		}
		signals = append(signals, signal)
	}
	events, err := t.admitSignals(process, signals, signalSourceExternal)
	if err != nil || len(events) == 0 {
		reply.send(processResponse{err: err})
		return
	}

	if err := t.startSignalCommit(process, reply, events); err != nil {
		reply.send(processResponse{err: err})
		t.failRuntime(err, process.handle.processID, EffectID{})
	}
}

func (t *treeRuntime) stageEvent(
	process *processState,
	name string,
	phase EventPhase,
	step uint64,
	effectID EffectID,
	payload json.RawMessage,
) {
	event := t.events.prepare(process, name, phase, step, effectID, payload)
	t.stageCommittedEvent(event)
}

func (t *treeRuntime) checkEventListenerReentrancy(ctx context.Context, operation string) error {
	return t.engine.observation.checkEventListenerReentrancy(ctx, t.rootID, operation)
}

func (t *treeRuntime) inspect(ctx context.Context) (TreeInspection, error) {
	select {
	case <-t.done:
		return t.finalInspection.clone(), nil
	default:
	}
	response := make(chan TreeInspection, 1)
	select {
	case t.inspections <- response:
	case <-t.done:
		return t.finalInspection.clone(), nil
	case <-ctx.Done():
		return TreeInspection{}, ctx.Err()
	}
	select {
	case inspection := <-response:
		return inspection, nil
	case <-t.done:
		return t.finalInspection.clone(), nil
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

func (t *treeRuntime) replyInspection(response chan TreeInspection) {
	response <- t.buildInspection()
}

func (t *treeRuntime) buildInspection() TreeInspection {
	inspection := TreeInspection{
		RootID: t.rootID, IncarnationID: t.writer.incarnation(), HeadDigest: t.writer.head().Digest(),
		CommitPending: t.writer.committing(), Freeze: t.freeze.phase(),
	}
	snapshots := t.writer.head().ProcessSnapshots()
	for _, snapshot := range snapshots {
		processID := snapshot.ProcessID()
		process := t.members.get(processID)
		if process == nil {
			continue
		}
		report := ProcessInspection{Snapshot: snapshot, Work: ProcessWorkIdle}
		_, runtimeErr := process.handle.outcome()
		report.RuntimeError, _ = errors.AsType[*RuntimeError](runtimeErr)
		if job := t.jobs.get(processID); job != nil {
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
		} else if t.runQueue.contains(processID) && !process.status.Terminal() {
			report.Work = ProcessWorkQueued
		}
		inspection.Processes = append(inspection.Processes, report)
	}
	return inspection
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
	t.events.emit(process, EventStepStarted, EventPhaseAttempt, sequence, EffectID{}, emptyEventPayload())
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
			processID: processID, attempt: attempt, result: result,
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
			processID: processID, attempt: attempt,
			result: restoreJobResult{execution: execution, err: err},
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
		observation := t.events.beginEffectAttempt(process, process.prepared.StepSequence, record.ID, EffectTargetFramework)
		operation, err := decodeFrameworkOperation(record.Effect.Payload())
		if err != nil {
			t.failProcessContract(process, failureCodeEngineFrameworkEffectSettlementInvalid, err)
			return
		}
		switch operation := operation.(type) {
		case waitOperation, childWaitOperation:
			t.settleFramework(process, record, observation)
		case childStartOperation:
			t.startChild(process, record, operation.spec, observation)
		case childControlOperation:
			t.controlChild(process, uint32(index), record, operation.request, observation)
		default:
			panic("agent: unhandled framework operation")
		}
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

		t.commitSettledEffect(process, batchIndex, *record)
	default:
		t.failProcessContract(
			process, failureCodeEngineEffectRecoveryInvalid, errInvalidReplayPolicy,
		)
	}
}

func (t *treeRuntime) startChild(
	process *processState,
	record *preparedEffect,
	spec ChildSpec,
	observation effectAttempt,
) {
	processID := process.handle.processID
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
			processID: processID, attempt: attempt, result: result,
		}
	}()
}

func (t *treeRuntime) startDispatch(
	process *processState,
	batchIndex uint32,
	record preparedEffect,
	reply processReply,
) {
	processID := process.handle.processID
	dispatcher := process.deployment.dispatcher

	attempt, ok := t.allocateAttempt(process)
	if !ok {
		return
	}
	request := t.effectRequestFor(process, batchIndex, record)
	observation := t.events.beginEffectAttempt(process, process.prepared.StepSequence, record.ID, EffectTargetDispatcher)
	request.attemptID = observation.id
	dispatchCtx, cancel := context.WithCancel(t.context)
	job := &processJob{
		kind:          processJobDispatch,
		attempt:       attempt,
		cancel:        cancel,
		effectID:      record.ID,
		effectAttempt: observation,
		reply:         reply,
	}
	t.setProcessJob(processID, job)
	deltas := t.events.deltas(process, record.ID, observation)

	go func() {
		settlement, err := dispatchEffect(dispatchCtx, dispatcher, request, deltas.emitter())
		dropped := deltas.close()
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
			result: dispatchJobResult{
				err:        err,
				effectID:   record.ID,
				settlement: settlement,
				dropped:    dropped,
			},
		}
	}()
}

func (t *treeRuntime) applyCompletion(completion treeJobCompletion) {
	process := t.members.get(completion.processID)
	if process == nil && t.jobs.get(completion.processID) != nil {
		panic("agent: owned work requires a tree member")
	}
	job, current := t.jobs.finish(completion)
	if !current {
		return
	}
	t.queueJoin(process)
	t.publishJobFinished(process, job, completion)
	if t.fault != nil {
		return
	}
	defer t.completeFreeze()
	if job.stale {
		t.retireStaleJob(process, completion.result.jobKind())
		return
	}
	t.adoptJobResult(process, job, completion)
	if t.writer.committing() {
		return
	}
	t.finishIfTerminal(process)
	if !process.status.Terminal() {
		t.enqueueProcess(completion.processID)
	}
}

// Attempt facts close even when the candidate is stale, cannot be committed,
// or a sibling has already stopped the runtime.
func (t *treeRuntime) publishJobFinished(process *processState, job *processJob, completion treeJobCompletion) {
	switch result := completion.result.(type) {
	case stepJobResult:
		t.events.stepFinished(process, result, job.stale || t.fault != nil)
	case dispatchJobResult:
		t.publishDispatchFinished(process, job, result)
	}
}

func (t *treeRuntime) retireStaleJob(process *processState, kind processJobKind) {
	// Resumption requires the committed state to be executable. Terminal
	// intent needs only its saved evidence, so reconstruction is unnecessary.
	if kind == processJobStep && !process.pendingControl.hasTerminalIntent() {
		t.startRestore(process)
	}
	if !t.freeze.engaged() {
		t.enqueueProcess(process.handle.processID)
	}
}

func (t *treeRuntime) adoptJobResult(process *processState, job *processJob, completion treeJobCompletion) {
	switch result := completion.result.(type) {
	case stepJobResult:
		t.applyStepCompletion(process, result)
	case restoreJobResult:
		if result.err != nil {
			t.failProcess(process, failureKindForError(result.err, FailureKindExecution), failureCodeExecutionSnapshotUnrestorable, result.err)
			return
		}
		process.execution = result.execution
	case dispatchJobResult:
		t.applyDispatchCompletion(process, job, result)
	case childStartJobResult:
		t.applyChildStartCompletion(process, job, result)
	}
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

	pending.event = t.events.settlement(parent,
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
	parent := t.members.get(pending.parentID)
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
			pending.plan.requestDigest,
			pending.plan.spec.Budget,
			pending.plan.spec.Capabilities,
			pending.result.startedAt)
		child := newProcessState(handle, pending.result.deployment, pending.result.execution, pending.result.state)
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
	t.events.publishSettlement(parent, effectID, EffectTargetFramework, status, observation, nil)
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

func (t *treeRuntime) applyStepCompletion(
	process *processState,
	result stepJobResult,
) {
	sequence := process.committedSteps + 1
	if result.err != nil {
		t.failStep(process, result)
		return
	}
	candidate, failure := process.prepareStep(result, t.treeLimits)
	if failure != nil {
		t.failProcess(process, failure.kind, failure.code, failure.cause)
		return
	}
	if err := t.validateChildWaitRelations(candidate); err != nil {
		t.failProcessContract(process, failureCodeExecutionEffectInvalid, err)
		return
	}
	if err := t.validateSnapshotCapacity(candidate); err != nil {
		t.failProcess(process, FailureKindExecution, failureCodeEngineLimitSnapshot, err)
		return
	}
	process.adoptCandidate(candidate)

	t.events.emit(process, EventStepPrepared, EventPhaseAttempt, sequence, EffectID{}, emptyEventPayload())
}

// A Strategy's StepError keeps its own classification only when Step itself
// returned it; snapshot, restore, and contained panics keep Engine codes.
func (t *treeRuntime) failStep(process *processState, result stepJobResult) {
	code := failureCodeExecutionStepFailed
	switch result.stage {
	case stepJobStageSnapshot:
		code = failureCodeExecutionSnapshotFailed
	case stepJobStageRestore:
		code = failureCodeExecutionSnapshotUnrestorable
	}
	sealed, ok := errors.AsType[*callbackError](result.err)
	if !ok || sealed == nil || result.stage != stepJobStageExecution || sealed.kind == FailureKindPanic || sealed.step == nil {
		t.failProcess(process, failureKindForError(result.err, FailureKindExecution), code, result.err)
		return
	}
	failure := sealed.step.Failure
	if !failure.Valid() {
		t.failProcess(process, FailureKindContract, code, ErrInvalidFailure)
		return
	}
	t.failProcess(process, failure.Kind(), failure.Code(), errors.New(failure.Message()))
}

// Relations are a tree fact, so prepareStep cannot check them on the Process.
func (t *treeRuntime) validateChildWaitRelations(candidate *processState) error {
	for _, record := range candidate.prepared.Effects {
		if record.Effect.Target() != EffectTargetFramework {
			continue
		}
		operation, err := decodeFrameworkOperation(record.Effect.Payload())
		if err != nil {
			return err
		}
		if wait, ok := operation.(childWaitOperation); ok {
			if err := wait.spec.validateRelations(candidate.handle.processID, t.members.relation); err != nil {
				return err
			}
		}
	}
	return nil
}

func (t *treeRuntime) publishDispatchFinished(process *processState, job *processJob, result dispatchJobResult) {
	process.counters.DroppedDeltas = saturatingCountAdd(process.counters.DroppedDeltas, result.dropped)
	t.events.dispatchFinished(process, job.effectAttempt, result)
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
	if record.unknown() {
		if result.err != nil || settlement.Status() == SettlementStatusUnknown {
			job.reply.send(processResponse{err: errors.Join(ErrEffectOutcomeUnknown, result.err)})
			return
		}
		t.commitResolution(process, settlement, job.reply)
		return
	}

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
	t.commitSettledEffect(process, uint32(index), process.prepared.Effects[index])
}

// A result can precede descendant cleanup. Publish each join once, from leaves
// upward, only after acknowledged outcomes and the owned calls have returned.
func (t *treeRuntime) publishJoins() bool {
	if t.writer.committing() || t.freeze.engaged() {
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
	if t.jobs.get(process.handle.processID) != nil {
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
	for _, childID := range t.members.childrenOf(process.handle.processID) {
		child := t.members.get(childID)
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
			processID: process.handle.processID, incarnationID: t.writer.incarnation(),
			headDigest: t.writer.head().Digest(), unresolvedEffectIDs: canonicalEffectIDs(unresolved), cause: t.fault,
		}
	}
	if !process.handle.finishJoin(joinErr) {
		return true
	}
	if joinErr == nil {
		t.notifyChildWaits(process.handle.processID, ChildWaitBoundaryDrained)
	}
	if parentID, child := process.handle.relation.ParentID(); child {
		t.queueJoin(t.members.get(parentID))
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
	child := t.members.get(processID)
	if child == nil {
		return
	}
	parentID, hasParent := child.handle.relation.ParentID()
	if !hasParent {
		return
	}
	parent := t.members.get(parentID)
	for registration := range t.childWaits.awaiting(parentID, processID, boundary) {
		if parent == nil || parent.status.Terminal() || parent.pendingControl.hasTerminalIntent() ||
			parent.mailbox.contains(registration.waitID.childWaitSignalID()) {
			continue
		}
		signal, satisfied, err := registration.satisfaction(&t.members)
		if err != nil {
			parent.recordFailure(FailureKindExecution, failureCodeEngineChildWaitSatisfactionEncodingFailed, err)
			t.stopProcessTree(parent)
			continue
		}
		if !satisfied {
			continue
		}
		if t.deliverChildWaitSatisfied(parent, signal) {
			t.enqueueProcess(parent.handle.processID)
		} else if parent.pendingControl.hasTerminalIntent() {
			t.stopProcessTree(parent)
		}
	}
}

func (t *treeRuntime) addProcess(process *processState) {
	if t == nil || process == nil || process.handle == nil ||
		process.handle.relation.RootID() != t.rootID {
		panic("agent: invalid tree Process")
	}
	t.members.add(process)
	process.handle.runtime.Store(t)
	t.queueJoin(process)
	if !process.status.Terminal() {
		t.enqueueProcess(process.handle.processID)
	}
}

// Only unpublished children can be removed while the owner is running.
func (t *treeRuntime) removeProcess(processID ProcessID) {
	if t.jobs.get(processID) != nil {
		panic("agent: cannot remove a Process with owned work")
	}
	t.members.remove(processID)
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
	case t.freezeCommands <- acquireFreezeCommand{acquisition: acquisition}:
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
		return t.writer.head(), true, nil
	default:
		return TreeSnapshot{}, false, nil
	}
}

// The tree owner installs wait registrations around one candidate adoption.
// A rejected local transition cannot leave a live registration behind.
// Only immediate child-wait answers and the resulting snapshot can exceed a
// bound here, so each failure is classified where it arises.
func (t *treeRuntime) finalizePrepared(process *processState) *stepPreparationFailure {
	processID := process.handle.processID
	finalization, err := newPreparedStepFinalization(process, process.prepared)
	if err == nil {
		err = finalization.prepareSettlements()
	}
	if err != nil {
		return newFinalizationFailure(failureCodeEngineLimitSnapshot, err)
	}
	immediate, err := t.registerOpenedChildWaits(processID, finalization)
	if err != nil {
		return newFinalizationFailure(failureCodeEngineLimitSnapshot, err)
	}
	adopted := false
	defer func() {
		if !adopted {
			for _, opened := range finalization.openedChildWaits {
				t.childWaits.remove(processID, opened.WaitID())
			}
		}
	}()
	if err = finalization.prepareTransition(time.Now().Round(0).UTC()); err != nil {
		return newFinalizationFailure(failureCodeEngineLimitSnapshot, err)
	}
	limitCode := failureCodeEngineLimitSnapshot
	candidate := process.candidate()
	candidate.adopt(finalization)
	if len(immediate) != 0 {
		limitCode = failureCodeEngineLimitChildWaitSignal
		if candidate, err = candidate.prepareSignals(immediate, signalSourceChildWait, t.treeLimits); err != nil {
			return newFinalizationFailure(limitCode, err)
		}
	}
	if err = t.validateSnapshotCapacity(candidate); err != nil {
		return newFinalizationFailure(limitCode, err)
	}
	process.adoptCandidate(candidate)
	adopted = true
	for _, waitID := range finalization.consumedChildWaits {
		t.childWaits.remove(processID, waitID)
	}
	for _, waitID := range finalization.commit.closedChildWaits {
		t.childWaits.remove(processID, waitID)
	}
	payload := marshalEventPayload(stepCommittedEventPayload{ProcessStatus: process.status})
	t.stageEvent(process, EventStepCommitted, EventPhaseCommitted, process.committedSteps, EffectID{}, payload)
	if process.status == StatusPaused {
		t.stageEvent(process, EventProcessPaused, EventPhaseCommitted, 0, EffectID{}, emptyEventPayload())
	}
	return nil
}

// registerOpenedChildWaits atomically registers every wait the prepared Step
// opens and returns the answers the current tree already satisfies. A failure
// removes only its own registrations, never a pre-existing one.
func (t *treeRuntime) registerOpenedChildWaits(processID ProcessID, finalization *preparedStepFinalization) ([]Signal, error) {
	var immediate []Signal
	for index, opened := range finalization.openedChildWaits {
		signal, satisfied, err := t.childWaits.register(processID, opened.WaitID(), opened.Spec(), &t.members)
		if err != nil {
			for _, registered := range finalization.openedChildWaits[:index] {
				t.childWaits.remove(processID, registered.WaitID())
			}
			return nil, err
		}
		if satisfied {
			immediate = append(immediate, signal)
		}
	}
	return immediate, nil
}

func (t *treeRuntime) terminatePreparedProcess(process *processState) {
	if t.jobs.get(process.handle.processID) != nil {
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
		t.childWaits.remove(process.handle.processID, waitID)
	}
}

func emptyEventPayload() json.RawMessage { return json.RawMessage("{}") }

func (t *treeRuntime) admitSignals(process *processState, signals []Signal, source signalSource) ([]eventFact, error) {
	candidate, err := process.prepareSignals(signals, source, t.treeLimits)
	if err != nil || candidate == nil {
		return nil, err
	}
	if err := t.validateSnapshotCapacity(candidate); err != nil {
		return nil, err
	}
	records := candidate.mailbox.records[len(process.mailbox.records):]
	process.adoptCandidate(candidate)
	return t.events.signalsAccepted(process, records), nil
}

func (t *treeRuntime) validateSnapshotCapacity(candidates ...*processState) error {
	quota := t.treeLimits.MaxSnapshotBytes
	if !quota.limited {
		// Without an aggregate quota, existing members already passed their own
		// admission and only candidates can change a Process quota. Start and
		// Restore pass no candidates and admit every member before publishing.
		checked := slices.Values(candidates)
		if len(candidates) == 0 {
			checked = t.members.substituted(nil)
		}
		for member := range checked {
			if _, err := member.snapshotAdmissionSize(t.treeLimits); err != nil {
				return err
			}
		}
		return nil
	}

	header, err := jsonv2.Marshal(t.treeSnapshotBase())
	if err != nil {
		return err
	}
	// The header contains an empty JSON array. Adding raw object encodings and
	// separators also reserves known wait settlements without validating a half-installed
	// child-control or child-wait transition.
	size := uint64(len(header))
	index := 0
	for member := range t.members.substituted(candidates) {
		memberSize, err := member.snapshotAdmissionSize(t.treeLimits)
		if err != nil {
			return err
		}
		var separator uint64
		if index > 0 {
			separator = 1
		}
		if !resourceQuantitiesFit(math.MaxUint64, size, memberSize, separator) {
			return ErrCounterExhausted
		}
		size += memberSize + separator
		if !quota.Allows(size) {
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
	t.events.publishSettlement(process, record.ID, EffectTargetFramework, record.Settlement.Status(), observation, nil)
	t.enqueueProcess(process.handle.processID)
}
