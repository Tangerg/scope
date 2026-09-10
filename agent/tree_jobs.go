package agent

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"time"
)

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
			process.discardPrepared()
			t.failProcess(process, FailureKindContract, "engine.effect.phase.invalid", err)
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
		if err := record.settleFramework(); err != nil {
			t.failPreparedEffect(process, "engine.framework_effect.settlement.invalid", err)
			return
		}
		t.publishSettlementEvent(process, record.ID, EffectTargetFramework, record.Settlement.Status(), startedAt)
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
		if settlementErr := record.settleUnknown(); settlementErr != nil {
			t.failPreparedEffect(process, "engine.framework_effect.settlement.invalid", settlementErr)
			return
		}
		t.publishSettlementEvent(process, record.ID, EffectTargetFramework, record.Settlement.Status(), startedAt)
		t.enqueueProcess(process.handle.processID)
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
	var deltaSequence atomic.Uint64
	var dropped atomic.Uint64
	var acceptingDeltas atomic.Bool
	acceptingDeltas.Store(true)
	emit := func(payload json.RawMessage) {
		if !acceptingDeltas.Load() {
			return
		}
		sequence := deltaSequence.Add(1)
		delta, err := newDelta(
			process.handle.processID, record.ID, t.incarnation,
			sequence, time.Now(), payload,
		)
		if err != nil || !t.engine.observation.offerDelta(t.context, delta) {
			dropped.Add(1)
		}
	}
	go func() {
		settlement, err := dispatchEffect(
			dispatchCtx,
			process.deployment.effectDispatcher(),
			request,
			emit,
		)
		acceptingDeltas.Store(false)
		if err != nil || !settlement.Valid() || settlement.EffectID() != record.ID {
			pending := record
			if settleErr := pending.settleUnknown(); settleErr == nil {
				settlement = *pending.Settlement
			} else {
				settlement = Settlement{}
			}
		}
		t.completions <- treeJobCompletion{
			processID: process.handle.processID,
			attempt:   attempt,
			kind:      processJobDispatch,
			dispatch: dispatchJobResult{
				effectID:   record.ID,
				settlement: settlement,
				dropped:    dropped.Load(),
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
	if err := t.applyChildOutcome(pending); err != nil {
		t.failPreparedEffect(parent, childSettlementInvalidCode, err)
		return
	}
	if t.engine.durability != nil {
		if event, ok := t.prepareSettlementEvent(parent,
			job.effectID, EffectTargetFramework,
			pending.childSettlementStatus(), job.startedAt,
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
		if err != nil {
			t.discardProspectiveChild(pending)
			t.failDurability(err, parent.handle.processID, job.effectID)
		}
		return
	}
	if err := t.publishChildOutcome(pending); err != nil {
		t.failPreparedEffect(parent, childSettlementInvalidCode, err)
	}
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
		t.engine.discardProcessStartReservation(pending.plan.childID)
		parent.releaseProvisionalChildBudget(pending.plan.spec.Budget)
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
	t.publishSettlementEvent(parent, effectID, EffectTargetFramework, status, startedAt)
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
	process.discardPrepared()
	t.failProcess(process, FailureKindContract, code, err)
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
		process.discardPrepared()
		t.failProcess(process, FailureKindContract, "engine.effect.settlement.invalid", err)
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
			record.ID, EffectTargetDispatcher, settlement.Status(), job.startedAt,
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
		job.startedAt,
	)
}
