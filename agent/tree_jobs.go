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
		process.fail(failure.kind, failure.code, failure.cause)
		t.finishIfTerminal(process)
		return
	}
	attempt, ok := t.nextAttempt(process)
	if !ok {
		return
	}
	sequence := process.committedSteps + 1
	process.publishEvent(t.context, EventStepStarted, EventPhaseAttempt, sequence, EffectID{}, emptyEventPayload())
	execution := process.execution
	process.execution = nil
	signals := process.mailbox.pending()
	stepCtx, cancel := context.WithCancel(context.Background())
	t.setProcessJob(process.controller.processID, &processJob{
		kind: processJobStep, attempt: attempt, cancel: cancel, startedAt: time.Now(),
	})
	go func() {
		transition, err := stepExecution(stepCtx, execution, signals)
		result := stepJobResult{transition: transition, stage: stepJobStageExecution, err: err}
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
			processID: process.controller.processID,
			attempt:   attempt,
			kind:      processJobStep,
			step:      result,
		}
	}()
}

func (t *treeRuntime) startNextEffect(process *processState) {
	for index := range process.prepared.wire.Effects {
		record := &process.prepared.wire.Effects[index]
		if record.Phase == effectPhaseSettled {
			continue
		}
		if record.Phase == effectPhasePlanned {
			if err := record.begin(); err != nil {
				process.discardPrepared()
				process.fail(FailureKindContract, "engine.effect.phase.invalid", err)
				return
			}
			if record.Effect.Target() == EffectTargetDispatcher && t.engine.durability != nil {
				if err := t.startPendingEffectCommit(process, uint32(index), *record); err != nil {
					t.failDurability(err, process.controller.processID, EffectID{})
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
			startedAt := process.publishEffectStarted(
				t.context, process.prepared.wire.StepSequence, record.ID, EffectTargetFramework,
			)
			operation, err := decodeFrameworkEffectOperation(record.Effect.Payload())
			if err == nil && operation == frameworkEffectStartChild {
				t.startChild(process, record, startedAt)
				return
			}
			if err := process.dispatchFrameworkEffect(t.context, record); err != nil {
				t.failPreparedEffect(process, "engine.framework_effect.settlement.invalid", err)
				return
			}
			process.publishSettlementEvent(
				t.context, record.ID, EffectTargetFramework, record.Settlement.Status(), startedAt,
			)
			t.markRunnable(process.controller.processID)
			return
		}
		t.startDispatch(process, uint32(index), *record)
		return
	}
}

func (t *treeRuntime) recoverPendingEffect(
	process *processState,
	batchIndex uint32,
	record *preparedEffectWire,
) {
	decision := process.restoredPending
	process.restoredPending = restoredPendingEffect{}
	switch decision.replayPolicy {
	case ReplayPolicySameIdentity:
		t.startDispatch(process, batchIndex, *record)
	case ReplayPolicyNever:
		if err := record.settleUnknown(); err != nil {
			t.failPreparedEffect(process, "engine.effect.recovery.invalid", err)
			return
		}
		if t.engine.durability == nil {
			t.markRunnable(process.controller.processID)
			return
		}
		settlement := *record.Settlement
		snapshot, err := t.captureTree()
		if err != nil {
			t.failDurability(err, process.controller.processID, record.ID)
			return
		}
		boundary, err := newEffectBoundary(
			EffectBoundarySettled,
			effectRequestFor(process, batchIndex, *record),
			settlement,
			t.head.digest(),
			snapshot,
		)
		if err != nil {
			t.failDurability(err, process.controller.processID, record.ID)
			return
		}
		t.startEffectCommit(&treeCommit{
			kind: treeCommitEffectSettled, processID: process.controller.processID,
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
	record *preparedEffectWire,
	startedAt time.Time,
) {
	spec, err := decodeChildStartEffect(record.Effect.Payload())
	if err != nil {
		if settlementErr := record.settleUnknown(); settlementErr != nil {
			t.failPreparedEffect(process, "engine.framework_effect.settlement.invalid", settlementErr)
			return
		}
		process.publishSettlementEvent(
			t.context, record.ID, EffectTargetFramework, record.Settlement.Status(), startedAt,
		)
		t.markRunnable(process.controller.processID)
		return
	}
	attempt, ok := t.nextAttempt(process)
	if !ok {
		return
	}
	preparation := process.prepareChildStart(record.ID, spec)
	if preparation.plan == nil {
		if err := t.settleChildStart(process, record.ID, preparation.result, startedAt); err != nil {
			t.failPreparedEffect(process, "engine.child.settlement.invalid", err)
			return
		}
		t.markRunnable(process.controller.processID)
		return
	}
	job := &processJob{
		kind: processJobChildStart, attempt: attempt, effectID: record.ID,
		childStart: preparation.plan, startedAt: startedAt,
	}
	t.setProcessJob(process.controller.processID, job)
	go func() {
		result := preparation.plan.execute(t.context)
		t.completions <- treeJobCompletion{
			processID:  process.controller.processID,
			attempt:    attempt,
			kind:       processJobChildStart,
			childStart: result,
		}
	}()
}

func (t *treeRuntime) startDispatch(
	process *processState,
	batchIndex uint32,
	record preparedEffectWire,
) {
	attempt, ok := t.nextAttempt(process)
	if !ok {
		return
	}
	request := newEffectRequest(
		process.controller.processID,
		process.controller.deploymentRef,
		process.controller.relation,
		process.prepared.wire.StepSequence,
		batchIndex,
		record.ID,
		record.Effect,
	)
	startedAt := process.publishEffectStarted(
		t.context, process.prepared.wire.StepSequence, record.ID, EffectTargetDispatcher,
	)
	job := &processJob{
		kind:      processJobDispatch,
		attempt:   attempt,
		effectID:  record.ID,
		startedAt: startedAt,
	}
	t.setProcessJob(process.controller.processID, job)
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
			process.controller.processID, record.ID, t.incarnation,
			sequence, time.Now(), payload,
		)
		if err != nil || !process.engine.observation.offerDelta(t.context, delta) {
			dropped.Add(1)
		}
	}
	go func() {
		settlement, err := dispatchEffect(
			t.context,
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
			processID: process.controller.processID,
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
	t.inflight.Add(-1)
	if job.cancel != nil {
		job.cancel()
	}
	if job.stale {
		if completion.kind == processJobStep {
			process.discardExecution()
		}
		if t.freeze == nil {
			t.markRunnable(completion.processID)
		}
		t.completeFreeze()
		return
	}
	switch completion.kind {
	case processJobStep:
		t.applyStepCompletion(process, job, completion.step)
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
		t.markRunnable(completion.processID)
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
			t.failPreparedEffect(parent, "engine.child.settlement.invalid", err)
		}
		return
	}
	pending := &pendingChildOutcome{
		parentID: parent.controller.processID, effectID: job.effectID,
		plan: plan, result: result, startedAt: job.startedAt,
	}
	if !result.admitted {
		if err := t.applyChildOutcome(pending); err != nil {
			t.failPreparedEffect(parent, "engine.child.settlement.invalid", err)
		}
		return
	}
	if t.engine.durability != nil {
		if err := t.applyChildOutcome(pending); err != nil {
			t.failDurability(err, parent.controller.processID, job.effectID)
			return
		}
		pending.prospectiveApplied = true
		if event, ok := parent.prepareSettlementEvent(
			job.effectID, EffectTargetFramework,
			pending.childSettlementStatus(), job.startedAt,
		); ok {
			pending.event = event
		}
		snapshot, err := t.captureTree()
		if err != nil {
			t.failDurability(err, parent.controller.processID, job.effectID)
			return
		}
		outcome := pending.treeOutcome(t.head.digest(), snapshot)
		t.startChildOutcomeCommit(pending, outcome, snapshot)
		return
	}
	if t.engine.startOutcomeAcknowledger != nil {
		t.startChildOutcomeCommit(pending, pending.lifecycleOutcome(), TreeSnapshot{})
		return
	}
	if err := t.applyChildOutcome(pending); err != nil {
		t.failPreparedEffect(parent, "engine.child.settlement.invalid", err)
		return
	}
	pending.prospectiveApplied = true
	if err := t.publishChildOutcome(pending); err != nil {
		t.failPreparedEffect(parent, "engine.child.settlement.invalid", err)
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
		controller := newProcessController(
			pending.plan.relation,
			pending.result.deployment.DeploymentRef(),
			pending.plan.spec.Budget,
			pending.plan.spec.Capabilities,
			pending.plan.treeLimits,
			pending.result.startedAt,
			StatusRunning,
		)
		controller.childRequestDigest = pending.plan.requestDigest
		child := newProcessState(
			pending.plan.engine, controller, pending.result.deployment, pending.result.execution,
			pending.result.state, pending.result.startedAt, pending.plan.limits,
		)
		t.addProcess(child)
	} else {
		pending.plan.engine.discardProcessStartReservation(pending.plan.childID)
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
	parent.publishSettlementEvent(
		t.context, effectID, EffectTargetFramework, status, startedAt,
	)
	return nil
}

func (t *treeRuntime) applyChildStartSettlement(
	parent *processState,
	effectID EffectID,
	result ChildStartResult,
) (SettlementStatus, error) {
	if parent.prepared == nil {
		return SettlementStatusInvalid, errors.New("prepared child-start Step is missing")
	}
	for index := range parent.prepared.wire.Effects {
		record := &parent.prepared.wire.Effects[index]
		if record.ID != effectID || record.Phase != effectPhasePending {
			continue
		}
		if err := settleChildStartRecord(record, effectID, result); err != nil {
			return SettlementStatusInvalid, err
		}
		return record.Settlement.Status(), nil
	}
	return SettlementStatusInvalid, errors.New("pending child-start Effect is missing")
}

func settleChildStartRecord(
	record *preparedEffectWire,
	effectID EffectID,
	result ChildStartResult,
) error {
	payload, err := encodeChildStartResult(result)
	if err != nil {
		return record.settleUnknown()
	}
	status := SettlementStatusSucceeded
	if _, failed := result.Failure(); failed {
		status = SettlementStatusFailed
	}
	settlement, err := NewSettlement(effectID, status, payload)
	if err != nil {
		return record.settleUnknown()
	}
	return record.settle(settlement)
}

func (t *treeRuntime) failPreparedEffect(process *processState, code string, err error) {
	process.discardPrepared()
	process.fail(FailureKindContract, code, err)
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
	process.publishEvent(t.context, EventStepFinished, EventPhaseAttempt, sequence, EffectID{}, payload)
	if result.err != nil {
		process.discardExecution()
		code := "execution.step.failed"
		switch result.stage {
		case stepJobStageSnapshot:
			code = "execution.snapshot.failed"
		case stepJobStageRestore:
			code = "execution.snapshot.unrestorable"
		}
		process.fail(failureKindForError(result.err), code, result.err)
		return
	}
	if failure := process.prepareStepResult(t.context, result); failure != nil {
		process.discardExecution()
		process.fail(failure.kind, failure.code, failure.cause)
	}
}

func (t *treeRuntime) applyDispatchCompletion(
	process *processState,
	job *processJob,
	result dispatchJobResult,
) {
	if process.prepared == nil {
		return
	}
	for index := range process.prepared.wire.Effects {
		record := &process.prepared.wire.Effects[index]
		if record.ID != result.effectID || record.Phase != effectPhasePending {
			continue
		}
		settlement := result.settlement
		if err := record.settle(settlement); err != nil {
			process.discardPrepared()
			process.fail(FailureKindContract, "engine.effect.settlement.invalid", err)
			return
		}
		var events []Event
		if result.dropped > 0 {
			process.usage.DroppedDeltas = saturatingCountAdd(
				process.usage.DroppedDeltas,
				result.dropped,
			)
			process.updateView()
			payload, _ := json.Marshal(deltaDroppedEventPayload{DroppedDeltaCount: result.dropped})
			if t.engine.durability != nil {
				if event, ok := process.prepareEvent(
					EventDeltaDropped, EventPhaseAttempt,
					process.prepared.wire.StepSequence, record.ID, payload,
				); ok {
					events = append(events, event)
				}
			} else {
				process.publishEvent(
					t.context, EventDeltaDropped, EventPhaseAttempt,
					process.prepared.wire.StepSequence, record.ID, payload,
				)
			}
		}
		if t.engine.durability != nil {
			if event, ok := process.prepareSettlementEvent(
				record.ID, EffectTargetDispatcher, settlement.Status(), job.startedAt,
			); ok {
				events = append(events, event)
			}
			snapshot, err := t.captureTree()
			if err != nil {
				t.failDurability(err, process.controller.processID, record.ID)
				return
			}
			request := effectRequestFor(process, uint32(index), *record)
			boundary, err := newEffectBoundary(
				EffectBoundarySettled, request, settlement, t.head.digest(), snapshot,
			)
			if err != nil {
				t.failDurability(err, process.controller.processID, record.ID)
				return
			}
			commit := &treeCommit{
				kind: treeCommitEffectSettled, processID: process.controller.processID,
				effectID: record.ID, snapshot: snapshot, events: events,
			}
			t.startEffectCommit(commit, boundary)
			return
		}
		process.publishSettlementEvent(
			t.context,
			record.ID,
			EffectTargetDispatcher,
			settlement.Status(),
			job.startedAt,
		)
		return
	}
}
