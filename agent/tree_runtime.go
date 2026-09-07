package agent

import (
	"context"
	"errors"
	"math"
	"sync/atomic"
	"time"
)

// treeRuntime is the sole Framework state committer for one root Process tree.
// Strategy reduction and external dispatch run as jobs; only their fenced
// completions may re-enter this owner line.
type treeRuntime struct {
	// Tree identity is fixed at publication; incarnation and head form the fence
	// carried by every later durability transition.
	engine      *Engine
	rootID      ProcessID
	incarnation TreeIncarnationID
	head        *treeHead

	// Atomics publish scheduling state outside the owner line. Freeze acquisition
	// publishes a stable checkpoint and head; commands and completions remain
	// the only mutation entrances back into the owner.
	inflight    atomic.Int64
	freezeHeld  atomic.Bool
	context     context.Context
	commands    chan treeCommand
	controls    chan treeCommand
	completions chan treeJobCompletion

	// Everything below is owner-line state. Keeping it lock-free makes commit,
	// scheduling, freeze, and checkpoint order a single explicit state machine.
	processes         map[ProcessID]*processState
	childWaits        map[WaitID]*childWaitRegistration
	runnable          []ProcessID
	queued            map[ProcessID]struct{}
	jobs              map[ProcessID]*processJob
	commit            *treeCommit
	commitDone        chan treeCommitCompletion
	fault             error
	checkpointPending map[ProcessID]checkpointPublication
	freeze            *activeTreeFreeze
	done              chan struct{}
}

type treeCommandKind uint8

const (
	treeCommandInvalid treeCommandKind = iota
	treeCommandProcess
	treeCommandAcquireFreeze
	treeCommandReleaseFreeze
	treeCommandApplyFreeze
)

type treeCommand struct {
	kind        treeCommandKind
	processID   ProcessID
	process     processCommand
	freeze      *treeFreeze
	acquisition *treeFreezeAcquisition
	projection  *treeStateProjection
	response    chan error
}

func newTreeProcessCommand(processID ProcessID, command processCommand) treeCommand {
	return treeCommand{kind: treeCommandProcess, processID: processID, process: command}
}

type treeFreezeAcquisition struct {
	response chan treeFreezeAcquisitionResult
	canceled chan struct{}
	mode     treeFreezeMode
}

type treeFreezeMode uint8

const (
	treeFreezeModeInvalid treeFreezeMode = iota
	treeFreezeModeSnapshot
	treeFreezeModeExclusive
)

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

type treeStateProjection struct {
	changes         []*preparedProcessStateChange
	childWaits      []*childWaitRegistration
	sourceDigest    Digest
	resultingDigest Digest
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
	parentID           ProcessID
	effectID           EffectID
	plan               *childStartPlan
	result             childStartJobResult
	startedAt          time.Time
	event              Event
	prospectiveApplied bool
}

type treeCommitCompletion struct {
	commit *treeCommit
	err    error
}

type checkpointPublication struct {
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
		engine:            engine,
		rootID:            rootID,
		context:           context.WithoutCancel(requireContext(ctx)),
		commands:          make(chan treeCommand, treeCommandBufferCapacity),
		controls:          make(chan treeCommand, treeCommandBufferCapacity),
		completions:       make(chan treeJobCompletion),
		processes:         make(map[ProcessID]*processState, len(processes)),
		childWaits:        make(map[WaitID]*childWaitRegistration),
		queued:            make(map[ProcessID]struct{}, len(processes)),
		jobs:              make(map[ProcessID]*processJob, len(processes)),
		commitDone:        make(chan treeCommitCompletion),
		checkpointPending: make(map[ProcessID]checkpointPublication),
		done:              make(chan struct{}),
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
	if t == nil || process == nil || process.controller == nil ||
		process.controller.relation.RootID() != t.rootID {
		panic("agent: invalid tree Process")
	}
	processID := process.controller.processID
	if t.processes[processID] != nil {
		panic("agent: duplicate tree Process")
	}
	process.runtime = t
	process.controller.runtime.Store(t)
	t.processes[processID] = process
	if !process.status.Terminal() {
		t.markRunnable(processID)
	}
}

func (t *treeRuntime) run(rootContext context.Context) {
	defer close(t.done)
	t.publishInitialProcessEvents()
	stopHostWatch := t.watchHostTermination(rootContext)
	defer stopHostWatch()

	for {
		if t.advanceReadyWork() {
			continue
		}
		if t.finished() {
			return
		}
		t.waitForWork()
	}
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
	return t.tryFreezeCancellation() || t.tryCommand() || t.tryCompletion() ||
		t.advanceOne() || t.tryStartCheckpoint()
}

func (t *treeRuntime) waitForWork() {
	if t.commit != nil {
		t.applyTreeCommitCompletion(<-t.commitDone)
		return
	}
	if t.freeze != nil && t.freeze.ready {
		select {
		case command := <-t.controls:
			t.applyCommand(command)
		case <-t.freeze.acquisition.canceled:
			t.releaseCurrentFreeze()
		}
		return
	}
	if freezeCanceled := t.freezeCanceled(); freezeCanceled != nil {
		select {
		case command := <-t.controls:
			t.applyCommand(command)
		case completion := <-t.completions:
			t.applyCompletion(completion)
		case <-freezeCanceled:
			t.releaseCurrentFreeze()
		}
		return
	}
	select {
	case command := <-t.controls:
		t.applyCommand(command)
	case command := <-t.commands:
		t.applyCommand(command)
	case completion := <-t.completions:
		t.applyCompletion(completion)
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

func (t *treeRuntime) tryCommand() bool {
	if t.commit != nil {
		return false
	}
	commands := t.commands
	if t.freeze != nil {
		commands = nil
	}
	select {
	case command := <-t.controls:
		t.applyCommand(command)
		return true
	case command := <-commands:
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

func (t *treeRuntime) markRunnable(processID ProcessID) {
	process := t.processes[processID]
	if t.fault != nil || process == nil || process.status.Terminal() || t.jobs[processID] != nil {
		return
	}
	if _, exists := t.queued[processID]; exists {
		return
	}
	t.queued[processID] = struct{}{}
	t.runnable = append(t.runnable, processID)
}

func (t *treeRuntime) popRunnable() *processState {
	for len(t.runnable) > 0 {
		processID := t.runnable[0]
		t.runnable = t.runnable[1:]
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
	process := t.popRunnable()
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
	if index < len(process.prepared.wire.Effects) {
		if process.prepared.wire.Effects[index].unknown() {
			if process.pendingControl.hasTerminalIntent() {
				unresolvedEffectIDs := process.unknownEffectIDs()
				process.discardPrepared()
				process.commitTerminationWithUnresolved(stepOutcome{}, unresolvedEffectIDs)
				t.finishIfTerminal(process)
			}
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
		t.markRunnable(process.controller.processID)
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
	t.inflight.Add(1)
}

func (t *treeRuntime) finished() bool {
	if t.freeze != nil || t.commit != nil || len(t.jobs) != 0 ||
		len(t.checkpointPending) != 0 {
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
