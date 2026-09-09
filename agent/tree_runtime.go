package agent

import (
	"context"
	"errors"
	"math"
	"sync/atomic"
	"time"
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
	childWaits          map[WaitID]*childWaitRegistration
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
		childWaits:          make(map[WaitID]*childWaitRegistration),
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

func (t *treeRuntime) addProcess(process *processState) {
	if t == nil || process == nil || process.handle == nil ||
		process.handle.relation.RootID() != t.rootID {
		panic("agent: invalid tree Process")
	}
	processID := process.handle.processID
	if t.processes[processID] != nil {
		panic("agent: duplicate tree Process")
	}
	process.runtime = t
	process.handle.runtime.Store(t)
	t.processes[processID] = process
	if !process.status.Terminal() {
		t.enqueueProcess(processID)
	}
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
	for _, process := range t.processesInCanonicalOrder() {
		if process.status.Terminal() {
			continue
		}
		if process.restored {
			process.publishEvent(t.context, EventProcessRestored, EventPhaseCommitted, 0, EffectID{}, emptyEventPayload())
		} else {
			process.publishEvent(t.context, EventProcessStarted, EventPhaseCommitted, 0, EffectID{}, emptyEventPayload())
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
	if t.commit != nil {
		controls = nil
		commands = nil
		completions = nil
		freezeCanceled = nil
	} else {
		commitDone = nil
		if t.freeze != nil {
			commands = nil
			if t.freeze.ready {
				completions = nil
			}
		}
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
	if t.commit != nil || t.freeze != nil {
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
	if t.commit != nil || t.freeze != nil && t.freeze.ready {
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
	if process.applyPendingControl(t.context) {
		t.finishIfTerminal(process)
		return true
	}
	if process.status == StatusRunning {
		t.startStep(process)
	}
	return true
}

func (t *treeRuntime) advancePrepared(process *processState) {
	index, err := process.prepared.wire.Effects.next()
	if err != nil {
		process.discardPrepared()
		process.fail(FailureKindContract, "engine.effect.phase.invalid", err)
		t.finishIfTerminal(process)
		return
	}
	if process.pendingControl.hasTerminalIntent() {
		t.stopProcessTree(process)
		if index < len(process.prepared.wire.Effects) {
			record := &process.prepared.wire.Effects[index]
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
		process.terminatePrepared()
		t.finishIfTerminal(process)
		return
	}
	if index < len(process.prepared.wire.Effects) {
		if process.prepared.wire.Effects[index].unknown() {
			return
		}
		t.startPreparedEffect(process, index)
		return
	}
	if err := process.finalizePrepared(t.context); err != nil {
		process.discardPrepared()
		process.fail(FailureKindContract, "engine.finalize.invalid", err)
	}
	t.finishIfTerminal(process)
	if !process.status.Terminal() {
		t.enqueueProcess(process.handle.processID)
	}
}

func (t *treeRuntime) nextAttempt(process *processState) (processAttempt, bool) {
	if process.attemptSequence == math.MaxUint64 {
		process.fail(
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
	if t.fault != nil {
		return true
	}
	for _, process := range t.processes {
		if !process.status.Terminal() {
			return false
		}
	}
	return true
}
