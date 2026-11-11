package run

import (
	"context"
	"errors"
	"math"
	"slices"
	"sync"
	"time"
)

const treeCommandCapacity = 32
const dispatchResultCapacity = 32

// Reasons for the controls the runtime originates rather than a Host.
const (
	reasonParentCompleted         = "parent completed"
	reasonContextCanceled         = "execution context canceled"
	reasonContextDeadlineExceeded = "execution context deadline exceeded"
)

type treeRuntime struct {
	runtime         *Runtime
	state           *treeState
	host            context.Context
	ctx             context.Context
	cancel          context.CancelFunc
	mu              sync.RWMutex
	commands        chan treeCommand
	results         chan jobResult
	done            chan struct{}
	stopRequested   chan struct{}
	stopOnce        sync.Once
	queue           []ProcessID
	jobs            map[EffectID]*dispatchJob
	steps           map[ProcessID]*stepJob
	emitters        map[EffectID]*deltaEmitter
	childQueue      []EffectID
	preparing       *childPreparation
	recovering      bool
	stopping        bool
	failure         error
	checkpointAfter uint64
	// commit is closed and replaced after every acknowledged append so local
	// readers wake immediately; it is nil once this writer stops.
	commit chan struct{}
}

func newTreeRuntime(runtime *Runtime, state *treeState, ctx context.Context) *treeRuntime {
	work, cancel := context.WithCancelCause(ctx)
	return &treeRuntime{runtime: runtime, state: state, host: ctx, ctx: work, cancel: func() { cancel(ErrWriterStopped) }, commands: make(chan treeCommand, treeCommandCapacity), results: make(chan jobResult, dispatchResultCapacity), done: make(chan struct{}), commit: make(chan struct{}), stopRequested: make(chan struct{}), jobs: make(map[EffectID]*dispatchJob), steps: make(map[ProcessID]*stepJob), emitters: make(map[EffectID]*deltaEmitter)}
}

func (t *treeRuntime) stop() { t.stopOnce.Do(func() { close(t.stopRequested); t.cancel() }) }

func (t *treeRuntime) failureError() error {
	t.mu.RLock()
	cause := t.failure
	t.mu.RUnlock()
	if cause == nil {
		cause = ErrClosed
	}
	return t.interrupted(cause)
}

func (t *treeRuntime) interrupted(cause error) error {
	t.mu.RLock()
	defer t.mu.RUnlock()
	var ids []EffectID
	for _, p := range t.state.processes {
		for id := range p.pending {
			ids = append(ids, id)
		}
	}
	return &RuntimeError{process: t.state.root, epoch: t.state.epoch, after: t.state.head, unresolved: canonicalEffectIDs(ids), cause: cause}
}

func (t *treeRuntime) append(ctx context.Context, records []Record) error {
	if len(records) == 0 {
		return nil
	}
	batch := Batch{Root: t.state.root, Epoch: t.state.epoch, After: t.state.head, Records: records}
	if err := batch.validateLimits(t.state.limits); err != nil {
		return err
	}
	commitCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), t.runtime.storeTimeout)
	defer cancel()
	if err := invokeCallbackErr("Store.Append", func() error { return t.runtime.store.Append(commitCtx, batch) }); err != nil {
		return err
	}
	if err := t.applyCommitted(batch); err != nil {
		return err
	}
	for _, record := range records {
		t.wake(record)
	}
	return nil
}

// wake schedules every process a committed record may have made runnable:
// the process it names, that process's parent, and a control's descendants.
func (t *treeRuntime) wake(record Record) {
	if settled, ok := record.(EffectSettled); ok && settled.Outcome.Status() != SettlementStatusUnknown {
		delete(t.emitters, settled.ID)
	}
	if control, ok := record.(ControlRequested); ok {
		for _, id := range t.state.ordered() {
			if control.Process.Contains(id) {
				t.enqueue(id)
			}
		}
	}
	process := recordProcess(record)
	if !process.Valid() {
		return
	}
	t.enqueue(process)
	if parent, ok := process.Parent(); ok {
		t.enqueue(parent)
	}
}

func (t *treeRuntime) applyCommitted(batch Batch) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, entry := range batch.Entries() {
		if err := t.state.apply(entry); err != nil {
			return err
		}
	}
	close(t.commit)
	t.commit = make(chan struct{})
	return nil
}

func (t *treeRuntime) nextCommit() <-chan struct{} {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.commit
}

// release wakes every local reader for the last time; they continue by polling
// because another writer may now own the tree.
func (t *treeRuntime) release() {
	t.mu.Lock()
	defer t.mu.Unlock()
	close(t.commit)
	t.commit = nil
}

func (t *treeRuntime) enqueue(id ProcessID) {
	p := t.state.processes[id]
	if p == nil || p.queued || p.exited != nil || t.steps[id] != nil {
		return
	}
	if !p.ready() && !p.terminal.Valid() {
		return
	}
	p.queued = true
	t.queue = append(t.queue, id)
}

func (t *treeRuntime) next() (ProcessID, bool) {
	for index := 0; index < len(t.queue); {
		id := t.queue[index]
		p := t.state.processes[id]
		if p != nil && t.checkpointDue() && !p.terminal.Valid() {
			index++
			continue
		}
		t.queue = slices.Delete(t.queue, index, index+1)
		if p != nil {
			p.queued = false
			return id, true
		}
	}
	if len(t.queue) == 0 {
		t.queue = nil
	}
	return ProcessID{}, false
}

// run is the tree's single owner goroutine. Pending events are served before
// new Steps so commands, results and stop requests are never starved by ready
// work; it returns once no owned work remains and the tree is halted or done.
func (t *treeRuntime) run() {
	defer close(t.done)
	defer t.release()
	defer t.cancel()
	if t.recovering {
		t.failOn(t.resumeEffects())
	}
	events := treeEvents{host: t.host.Done(), stop: t.stopRequested}
	for !t.finished() {
		if t.accepting() && (t.servePending(&events) || t.advanceNext()) {
			continue
		}
		t.awaitEvent(&events)
	}
}

// treeEvents are the owner's one-shot inputs; each is cleared once handled.
type treeEvents struct {
	host <-chan struct{}
	stop <-chan struct{}
}

func (t *treeRuntime) accepting() bool { return !t.stopping && t.failure == nil }

func (t *treeRuntime) idle() bool {
	return len(t.jobs) == 0 && len(t.steps) == 0 && t.preparing == nil
}

func (t *treeRuntime) finished() bool {
	if !t.idle() {
		return false
	}
	root := t.state.processes[t.state.root]
	return !t.accepting() || root != nil && root.exited != nil
}

func (t *treeRuntime) servePending(events *treeEvents) bool {
	select {
	case <-events.stop:
		t.stopRequestedWork(events)
		return true
	default:
	}
	if t.observeHostCancellation(events) {
		return true
	}
	// An always-busy mailbox must not postpone a quiescent snapshot forever.
	if t.checkpointDue() && len(t.steps) == 0 {
		t.failOn(t.checkpoint())
		return true
	}
	select {
	case command := <-t.commands:
		t.command(command)
	case result := <-t.results:
		t.completed(result)
	default:
		return false
	}
	return true
}

func (t *treeRuntime) awaitEvent(events *treeEvents) {
	select {
	case command := <-t.commands:
		t.command(command)
	case result := <-t.results:
		t.completed(result)
	case <-events.stop:
		t.stopRequestedWork(events)
	case <-events.host:
		events.host = nil
		if !t.stopping {
			t.failOn(t.contextCanceled())
		}
	}
}

func (t *treeRuntime) stopRequestedWork(events *treeEvents) {
	if events.host != nil && t.host.Err() != nil {
		events.host = nil
		t.failOn(t.contextCanceled())
	}
	t.stopWork()
	events.stop = nil
}

func (t *treeRuntime) observeHostCancellation(events *treeEvents) bool {
	if events.host == nil || t.host.Err() == nil {
		return false
	}
	events.host = nil
	t.failOn(t.contextCanceled())
	return true
}

func (t *treeRuntime) advanceNext() bool {
	id, ready := t.next()
	if ready {
		t.failOn(t.advance(id))
	}
	return ready
}

func (t *treeRuntime) stopWork() {
	if t.stopping {
		return
	}
	t.stopping = true
	t.cancel()
	for _, job := range t.jobs {
		job.cancel()
	}
}

func (t *treeRuntime) failOn(err error) {
	if err != nil {
		t.fail(err)
	}
}

func (t *treeRuntime) fail(err error) {
	t.mu.Lock()
	if t.failure == nil {
		t.failure = err
	}
	t.mu.Unlock()
	t.cancel()
	for _, job := range t.jobs {
		job.cancel()
	}
}

func (t *treeRuntime) advance(id ProcessID) error {
	p := t.state.processes[id]
	if p == nil || p.exited != nil {
		return nil
	}
	if p.terminal.Valid() {
		return t.finish(p)
	}
	if !p.ready() || t.steps[id] != nil {
		return nil
	}
	if p.usage.CommittedSteps == math.MaxUint64 {
		return t.reject(p, newRuntimeFailure(FailureKindExecution, failureCodeRuntimeCounterExhausted, ErrCounterExhausted))
	}
	turn := Turn{Process: id, Step: p.usage.CommittedSteps + 1, Messages: slices.Clone([]Message(p.inbox))}
	ctx, cancel := context.WithCancel(t.ctx)
	t.steps[id] = &stepJob{turn: turn, cancel: cancel}
	execution := p.execution
	go func() {
		transition, err := stepExecution(restoreContext{Context: ctx}, execution, turn)
		t.results <- stepResult{process: id, transition: transition, err: err}
	}()
	return nil
}

func (t *treeRuntime) completed(result jobResult) {
	switch value := result.(type) {
	case dispatchResult:
		t.dispatched(value)
	case stepResult:
		t.stepped(value)
	case childResult:
		t.childPrepared(value)
	}
}

func (t *treeRuntime) stepped(result stepResult) {
	job := t.steps[result.process]
	if job == nil {
		t.fail(ErrInvalidTransition)
		return
	}
	delete(t.steps, result.process)
	job.cancel()
	if t.failure != nil || t.stopping {
		return
	}
	p := t.state.processes[result.process]
	if p == nil {
		t.fail(ErrProcessNotFound)
		return
	}
	if p.terminal.Valid() {
		p.execution = nil
		t.enqueue(result.process)
		return
	}
	if result.err != nil {
		if t.ctx.Err() != nil {
			t.failOn(t.contextCanceled())
			return
		}
		failure, classified := StepFailure(result.err)
		if !classified {
			failure = newRuntimeFailure(failureKindForError(result.err, FailureKindExecution), failureCodeExecutionStepFailed, result.err)
		}
		t.failOn(t.reject(p, failure))
		return
	}
	if int(result.transition.Consumed()) > len(job.turn.Messages) {
		t.failOn(t.reject(p, newRuntimeFailure(FailureKindContract, failureCodeExecutionTransitionInvalid, ErrInvalidTransition)))
		return
	}
	t.failOn(t.commitStep(p, job.turn, result.transition))
}

func (t *treeRuntime) commitStep(p *processState, turn Turn, transition Transition) error {
	if err := p.admitTransition(transition, t.state.limits, uint32(len(turn.Messages))); err != nil {
		failure, classified := StepFailure(err)
		if !classified {
			failure = newRuntimeFailure(FailureKindContract, failureCodeExecutionTransitionInvalid, err)
		}
		return t.reject(p, failure)
	}
	records := []Record{Stepped{Process: turn.Process, Step: turn.Step, Delivered: uint32(len(turn.Messages)), Transition: transition}}
	for index, effect := range transition.effects {
		policy, err := t.policy(p, effect)
		if err != nil {
			code := failureCodeExecutionEffectInvalid
			if _, callback := errors.AsType[*callbackError](err); !callback && errors.Is(err, ErrInvalidCapability) {
				code = failureCodeRuntimeCapabilityDenied
			}
			return t.reject(p, newRuntimeFailure(failureKindForError(err, FailureKindContract), code, err))
		}
		records = append(records, EffectIntent{ID: turn.EffectID(uint32(index)), Effect: effect, Policy: policy, At: canonicalTime(time.Now())})
	}
	for _, message := range turn.Messages[:transition.consumed] {
		if exit, ok := message.(Exit); ok {
			records = append(records, Reaped{Process: exit.Child})
		}
	}
	if err := t.append(t.ctx, records); err != nil {
		return err
	}
	for index := range transition.effects {
		if err := t.dispatch(turn.EffectID(uint32(index)), nil); err != nil {
			return err
		}
	}
	if !t.checkpointDue() {
		t.checkpointAfter++
	}
	return nil
}

func (t *treeRuntime) policy(p *processState, effect Effect) (EffectPolicy, error) {
	if intrinsic, owned := effect.intrinsicPolicy(); owned {
		return intrinsic, nil
	}
	if p.program.dispatcher == nil {
		return EffectPolicy{}, ErrInvalidEffect
	}
	policy, err := invokeCallback("Dispatcher.Policy", func() (EffectPolicy, error) { return p.program.dispatcher.Policy(effect), nil })
	if err != nil {
		return EffectPolicy{}, err
	}
	if !policy.Valid() {
		return EffectPolicy{}, ErrInvalidEffect
	}
	if !p.started.Capabilities.Allows(policy.RequiredCapabilities) {
		return EffectPolicy{}, ErrInvalidCapability
	}
	return policy, nil
}

func (t *treeRuntime) reject(p *processState, failure Failure) error {
	return t.append(t.ctx, []Record{ExecutionFailed{Process: p.started.Process, Failure: failure, At: canonicalTime(time.Now())}})
}

func (t *treeRuntime) finish(p *processState) error {
	if t.steps[p.started.Process] != nil {
		return nil
	}
	var records []Record
	for _, id := range t.state.children(p.started.Process) {
		child := t.state.processes[id]
		if child.exited != nil {
			records = append(records, Reaped{Process: id})
		} else if !child.terminal.Valid() {
			records = append(records, ControlRequested{Origin: ControlOriginParent, Process: id, Kind: ControlCancel, Reason: reasonParentCompleted, At: canonicalTime(time.Now())})
		}
	}
	if len(records) > 0 {
		if err := t.append(t.ctx, records); err != nil {
			return err
		}
	}
	t.cancelJobs(p.started.Process)
	if err := t.startNextChild(); err != nil {
		return err
	}
	if p.activeChildren != 0 || p.retainedChildren != 0 || len(p.pending) != 0 {
		return nil
	}
	return t.append(t.ctx, []Record{Exited{Process: p.started.Process, Termination: p.terminal, At: canonicalTime(time.Now())}})
}

func (t *treeRuntime) cancelJobs(root ProcessID) {
	if t.preparing != nil && root.Contains(t.preparing.id.Process) {
		t.preparing.cancel()
	}
	for id, job := range t.steps {
		if root.Contains(id) {
			job.cancel()
		}
	}
	for id, job := range t.jobs {
		if root.Contains(id.Process) {
			job.cancel()
		}
	}
}

func (t *treeRuntime) checkpointDue() bool {
	return t.checkpointAfter >= t.runtime.checkpointInterval
}

func (t *treeRuntime) checkpoint() error {
	checkpoint, err := t.state.checkpoint()
	if err != nil {
		return err
	}
	if err := t.append(t.ctx, []Record{checkpoint}); err != nil {
		return err
	}
	t.checkpointAfter = 0
	return nil
}

// resumeEffects decides every Effect a crash interrupted, in declaration
// order so that two writers recovering the same log append the same records.
func (t *treeRuntime) resumeEffects() error {
	for _, id := range t.state.ordered() {
		p := t.state.processes[id]
		for _, pending := range p.orderedPending() {
			if pending.Outcome != nil {
				continue
			}
			if err := t.resumeEffect(p, pending.Intent); err != nil {
				return err
			}
		}
	}
	return nil
}

// resumeEffect cancels a child start its finished parent no longer wants,
// settles Unknown an Effect that may not repeat, and redispatches the rest.
func (t *treeRuntime) resumeEffect(p *processState, intent EffectIntent) error {
	switch {
	case intent.Effect.Target() == EffectTargetFramework && p.terminal.Valid():
		return t.settleChildRequest(intent.ID, nil, newRuntimeFailure(FailureKindExecution, failureCodeRuntimeChildRequestCanceled, ErrProcessFinished))
	case intent.Policy.Replay == ReplayPolicyNever || p.terminal.Valid():
		failure := newRuntimeFailure(FailureKindExternal, failureCodeRuntimeEffectRecoveryInvalid, ErrEffectOutcomeUnknown)
		return t.append(t.ctx, []Record{unknownRecord(intent.ID, failure)})
	default:
		return t.dispatch(intent.ID, nil)
	}
}

func (t *treeRuntime) dispatch(id EffectID, reply chan commandResponse) error {
	p := t.state.processes[id.Process]
	if p == nil {
		return ErrProcessNotFound
	}
	pending, ok := p.pending[id]
	if !ok {
		return ErrEffectNotPending
	}
	if p.terminal.Valid() {
		return nil
	}
	if pending.Intent.Effect.spawn != nil {
		t.childQueue = append(t.childQueue, id)
		return t.startNextChild()
	}
	if pending.Intent.Effect.Target() == EffectTargetFramework {
		return t.framework(p, pending.Intent)
	}
	ctx, cancel := context.WithCancel(t.ctx)
	request := EffectRequest{id: id, epoch: t.state.epoch, program: p.started.Program, effect: pending.Intent.Effect}
	job := &dispatchJob{cancel: cancel, reply: reply}
	t.jobs[id] = job
	dispatcher := p.program.dispatcher
	emit := t.emitter(request)
	go func() {
		outcome, err := invokeCallback("Dispatcher.Dispatch", func() (Settlement, error) { return dispatcher.Dispatch(ctx, request, emit) })
		var failure *Failure
		if err != nil {
			value := dispatchFailure(err)
			failure = &value
			outcome = unknownSettlement()
		}
		if !outcome.Valid() {
			value := newRuntimeFailure(FailureKindContract, failureCodeRuntimeDispatchSettlementInvalid, ErrInvalidSettlement)
			failure = &value
			outcome = unknownSettlement()
		}
		t.results <- dispatchResult{id: id, outcome: outcome, failure: failure}
	}()
	return nil
}

func (t *treeRuntime) dispatched(result dispatchResult) {
	job := t.jobs[result.id]
	if job == nil {
		t.fail(ErrInvalidEffect)
		return
	}
	delete(t.jobs, result.id)
	job.cancel()
	if t.failure != nil {
		if job.reply != nil {
			job.reply <- commandResponse{err: t.failure}
		}
		return
	}
	err := t.append(t.ctx, []Record{EffectSettled{ID: result.id, Outcome: result.outcome, Failure: result.failure, At: canonicalTime(time.Now())}})
	if err != nil {
		t.fail(err)
	}
	if job.reply != nil {
		if err == nil && result.outcome.Status() == SettlementStatusUnknown {
			err = ErrEffectOutcomeUnknown
		}
		job.reply <- commandResponse{err: err}
	}
}

// framework applies a Send or Cancel to a direct child inside the tree and
// settles it in the same batch. It performs no external I/O, so its outcome is
// always definite.
func (t *treeRuntime) framework(parent *processState, intent EffectIntent) error {
	records, err := t.childControl(parent, intent.Effect)
	var outcome Settlement
	if err != nil {
		records = nil
		outcome, err = NewFailedSettlement(newRuntimeFailure(failureKindForError(err, FailureKindContract), failureCodeRuntimeChildRequestInvalid, err), Payload{})
	} else {
		outcome, err = appliedSettlement()
	}
	if err != nil {
		return err
	}
	records = append(records, EffectSettled{ID: intent.ID, Outcome: outcome, At: canonicalTime(time.Now())})
	if err := t.append(t.ctx, records); err != nil {
		return err
	}
	if cancel := intent.Effect.cancel; cancel != nil {
		t.cancelJobs(cancel.Child)
	}
	return nil
}

func (t *treeRuntime) childControl(parent *processState, effect Effect) ([]Record, error) {
	switch {
	case effect.send != nil:
		child, err := t.state.child(parent.started.Process, effect.send.Child)
		if err != nil {
			return nil, err
		}
		return t.signals(child, []Signal{effect.send.Signal})
	case effect.cancel != nil:
		child, err := t.state.child(parent.started.Process, effect.cancel.Child)
		if err != nil || child.exited != nil {
			return nil, err
		}
		return []Record{ControlRequested{Origin: ControlOriginParent, Process: effect.cancel.Child, Kind: ControlCancel, Reason: effect.cancel.Reason, At: canonicalTime(time.Now())}}, nil
	default:
		return nil, ErrInvalidEffect
	}
}

// Preparing one child at a time avoids a second authority for grants and paths.
// The actor still handles controls and inbox admissions while callbacks run.
func (t *treeRuntime) startNextChild() error {
	if t.preparing != nil || t.stopping || t.failure != nil {
		return nil
	}
	for len(t.childQueue) > 0 {
		id := t.childQueue[0]
		t.childQueue = t.childQueue[1:]
		parent := t.state.processes[id.Process]
		pending, ok := parent.pending[id]
		if !ok {
			return ErrEffectNotPending
		}
		if parent.terminal.Valid() {
			if err := t.settleChildRequest(id, nil, newRuntimeFailure(FailureKindExecution, failureCodeRuntimeChildRequestCanceled, ErrProcessFinished)); err != nil {
				return err
			}
			continue
		}
		spec := *pending.Intent.Effect.spawn
		if _, err := t.state.admitChild(parent, spec); err != nil {
			if settleErr := t.settleChildRequest(id, nil, newRuntimeFailure(FailureKindContract, failureCodeRuntimeChildRequestInvalid, err)); settleErr != nil {
				return settleErr
			}
			continue
		}
		ctx, cancel := context.WithCancel(t.ctx)
		t.preparing = &childPreparation{id: id, cancel: cancel}
		program, process := parent.program, parent.started.Process
		go func() { t.results <- t.prepareChild(ctx, id, program, process, spec) }()
		return nil
	}
	t.childQueue = nil
	return nil
}

func (t *treeRuntime) prepareChild(ctx context.Context, id EffectID, parent Program[Payload, Payload], process ProcessID, spec ChildSpec) childResult {
	rejected := func(err error, kind FailureKind, code string) childResult {
		return childResult{id: id, failure: newRuntimeFailure(failureKindForError(err, kind), code, err)}
	}
	child, err := process.Child(spec.Key)
	if err != nil {
		return rejected(err, FailureKindContract, failureCodeRuntimeChildRequestInvalid)
	}
	program, err := t.runtime.resolve(parent, spec.ProgramRef)
	if err != nil {
		return rejected(err, FailureKindContract, failureCodeRuntimeChildRequestInvalid)
	}
	if inputErr := program.descriptor.ValidateInput(spec.Input); inputErr != nil {
		return rejected(inputErr, FailureKindContract, failureCodeRuntimeChildRequestInvalid)
	}
	admission := ProcessAdmission{process: child, program: program.ProgramRef(), descriptor: program.descriptor, budget: spec.Budget, capabilities: spec.Capabilities}
	if admissionErr := requestProcessAdmission(ctx, t.runtime.admitter, admission); admissionErr != nil {
		return rejected(admissionErr, FailureKindExternal, failureCodeRuntimeChildAdmissionRejected)
	}
	execution, failure, err := initializeExecution(ctx, program.definition, spec.Input)
	if err != nil {
		if !failure.Valid() {
			return rejected(err, FailureKindExecution, failureCodeRuntimeChildRequestCanceled)
		}
		return childResult{id: id, failure: failure}
	}
	return childResult{id: id, child: &preparedChild{started: Started{Process: child, Program: program.ProgramRef(), Input: spec.Input, Budget: spec.Budget, Capabilities: spec.Capabilities, At: canonicalTime(time.Now())}, program: program, execution: execution}}
}

func (t *treeRuntime) childPrepared(result childResult) {
	if t.preparing == nil || t.preparing.id != result.id {
		t.fail(ErrInvalidChildStart)
		return
	}
	t.preparing.cancel()
	t.preparing = nil
	if t.failure != nil || t.stopping {
		return
	}
	parent := t.state.processes[result.id.Process]
	if parent.terminal.Valid() {
		result.child = nil
		result.failure = newRuntimeFailure(FailureKindExecution, failureCodeRuntimeChildRequestCanceled, ErrProcessFinished)
	} else if result.child != nil {
		spec := parent.pending[result.id].Intent.Effect.spawn
		if _, err := t.state.admitChild(parent, *spec); err != nil {
			result.child = nil
			result.failure = newRuntimeFailure(FailureKindContract, failureCodeRuntimeChildRequestInvalid, err)
		}
	}
	if err := t.settleChildRequest(result.id, result.child, result.failure); err != nil {
		t.fail(err)
		return
	}
	t.failOn(t.startNextChild())
}

func (t *treeRuntime) settleChildRequest(id EffectID, child *preparedChild, failure Failure) error {
	var records []Record
	var outcome Settlement
	var err error
	if child != nil {
		records = append(records, child.started)
		outcome, err = spawnedSettlement(child.started.Process)
	} else {
		outcome, err = NewFailedSettlement(failure, Payload{})
	}
	if err != nil {
		return err
	}
	records = append(records, EffectSettled{ID: id, Outcome: outcome, At: canonicalTime(time.Now())})
	if err := t.append(t.ctx, records); err != nil {
		return err
	}
	if child != nil {
		p := t.state.processes[child.started.Process]
		p.program, p.execution = child.program, child.execution
		t.enqueue(child.started.Process)
	}
	return nil
}

func (t *treeRuntime) signals(p *processState, signals []Signal) ([]Record, error) {
	fresh, err := signalBatch(signals).unadmitted(p.receipts)
	if err != nil {
		return nil, err
	}
	var records []Record
	for _, signal := range fresh {
		if err := p.admissible(signal); err != nil {
			return nil, err
		}
		records = append(records, SignalAdmitted{Process: p.started.Process, Signal: signal, At: canonicalTime(time.Now())})
	}
	if err := p.admitMessages(t.state.limits, 0, nil, uint64(len(records))); err != nil {
		return nil, err
	}
	return records, nil
}

func (t *treeRuntime) command(request treeCommand) {
	p := t.state.processes[request.process]
	switch {
	case !t.accepting():
		request.reply <- commandResponse{err: ErrClosed}
		return
	case p == nil:
		request.reply <- commandResponse{err: ErrProcessNotFound}
		return
	}
	switch operation := request.operation.(type) {
	case replayCommand:
		t.replay(request, p, operation.id)
	case recordingCommand:
		records, err := operation.records(t, p)
		if err == nil {
			err = t.commitRequested(request, records)
		}
		request.reply <- commandResponse{accepted: err == nil && len(records) > 0, err: err}
	default:
		request.reply <- commandResponse{err: ErrInvalidProcessControl}
	}
}

// commitRequested appends a Handle's records. Only a request over its resource
// limit is the caller's error; any other append failure stops this writer.
func (t *treeRuntime) commitRequested(request treeCommand, records []Record) error {
	err := t.append(request.ctx, records)
	if err != nil {
		_, callback := errors.AsType[*callbackError](err)
		if callback || !errors.Is(err, ErrResourceLimitExceeded) {
			t.fail(err)
		}
		return err
	}
	if control, ok := request.operation.(controlCommand); ok && (control.kind == ControlCancel || control.kind == ControlKill) {
		t.cancelJobs(request.process)
	}
	return nil
}

// replay redispatches an Unknown Effect that its policy allows to repeat; the
// reply waits for the new settlement.
func (t *treeRuntime) replay(request treeCommand, p *processState, id EffectID) {
	pending, ok := p.pending[id]
	switch {
	case !ok || pending.Outcome == nil || t.jobs[id] != nil:
		request.reply <- commandResponse{err: ErrEffectNotPending}
	case pending.Intent.Policy.Replay != ReplayPolicySameIdentity || p.terminal.Valid():
		request.reply <- commandResponse{err: ErrEffectReplayForbidden}
	default:
		if err := t.dispatch(id, request.reply); err != nil {
			request.reply <- commandResponse{err: err}
		}
	}
}

type dispatchJob struct {
	cancel context.CancelFunc
	reply  chan commandResponse
}

type dispatchResult struct {
	id      EffectID
	outcome Settlement
	failure *Failure
}

func unknownSettlement() Settlement {
	return Settlement{status: SettlementStatusUnknown, payload: nullPayload}
}

func unknownRecord(id EffectID, failure Failure) EffectSettled {
	return EffectSettled{ID: id, Outcome: unknownSettlement(), Failure: &failure, At: canonicalTime(time.Now())}
}

type preparedChild struct {
	started   Started
	program   Program[Payload, Payload]
	execution Execution
}

func (t *treeRuntime) contextCanceled() error {
	// Closing a local writer cancels its workers, not the durable computation.
	if t.host.Err() == nil {
		return nil
	}
	kind, reason := ControlCancel, reasonContextCanceled
	if errors.Is(t.host.Err(), context.DeadlineExceeded) {
		kind, reason = ControlDeadline, reasonContextDeadlineExceeded
	}
	if root := t.state.processes[t.state.root]; root == nil || root.exited != nil {
		return nil
	}
	err := t.append(t.ctx, []Record{ControlRequested{Origin: ControlOriginHost, Process: t.state.root, Kind: kind, Reason: reason, At: canonicalTime(time.Now())}})
	t.cancelJobs(t.state.root)
	return err
}

type jobResult interface{ jobResult() }

func (dispatchResult) jobResult() {}

type stepJob struct {
	turn   Turn
	cancel context.CancelFunc
}

type stepResult struct {
	process    ProcessID
	transition Transition
	err        error
}

func (stepResult) jobResult() {}

func (t *treeRuntime) emitter(request EffectRequest) DeltaEmitter {
	if t.runtime.deltas == nil {
		return discardDelta
	}
	emitter := t.emitters[request.ID()]
	if emitter == nil {
		emitter = &deltaEmitter{request: request, channel: t.runtime.deltas}
		t.emitters[request.ID()] = emitter
	}
	return emitter.emit
}

type childPreparation struct {
	id     EffectID
	cancel context.CancelFunc
}

type childResult struct {
	id      EffectID
	child   *preparedChild
	failure Failure
}

func (childResult) jobResult() {}
