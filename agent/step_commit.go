package agent

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

type stepPreparationFailure struct {
	kind  FailureKind
	code  string
	cause error
}

func (p *processState) stepSchedulingFailure() *stepPreparationFailure {
	reservedBudget := p.effectiveReservedBudget()
	if resourceQuantitiesFit(p.limits.MaxSteps, p.committedSteps, 1) &&
		resourceQuantitiesFit(p.budget.Steps, p.committedSteps, reservedBudget.Steps, 1) {
		return nil
	}
	return &stepPreparationFailure{
		kind: FailureKindExecution, code: "engine.limit.steps", cause: ErrResourceLimitExceeded,
	}
}

func (p *processState) prepareStepResult(
	ctx context.Context,
	result stepJobResult,
) *stepPreparationFailure {
	transition := result.transition
	signals := p.mailbox.pending()
	if !transition.Valid() || uint64(transition.ConsumedSignals()) > uint64(len(signals)) {
		return &stepPreparationFailure{
			kind: FailureKindContract, code: "execution.transition.invalid", cause: ErrInvalidTransition,
		}
	}
	for _, effect := range transition.Effects() {
		if !p.capabilities.Allows(effect.RequiredCapabilities()) {
			return &stepPreparationFailure{
				kind: FailureKindContract, code: "engine.capability.denied", cause: ErrInvalidCapability,
			}
		}
	}
	effectCount := uint64(len(transition.Effects()))
	reservedBudget := p.effectiveReservedBudget()
	if !resourceQuantitiesFit(p.limits.MaxEffects, p.usage.PreparedEffects, effectCount) ||
		!resourceQuantitiesFit(
			p.budget.Effects, p.usage.PreparedEffects, reservedBudget.Effects, effectCount,
		) {
		return &stepPreparationFailure{
			kind: FailureKindExecution, code: "engine.limit.effects", cause: ErrResourceLimitExceeded,
		}
	}
	remainingPending := p.mailbox.pendingCount() - uint64(transition.ConsumedSignals())
	if !resourceQuantitiesFit(p.limits.MaxSignals, p.usage.AcceptedSignals, effectCount) ||
		!resourceQuantitiesFit(p.limits.MaxPendingSignals, remainingPending, effectCount) ||
		!resourceQuantitiesFit(
			p.budget.Signals, p.usage.AcceptedSignals, reservedBudget.Signals, effectCount,
		) {
		return &stepPreparationFailure{
			kind: FailureKindExecution, code: "engine.limit.signals", cause: ErrResourceLimitExceeded,
		}
	}
	if output, completes := transition.Output(); completes {
		if validateOutputErr := p.deployment.Descriptor().ValidateOutput(output); validateOutputErr != nil {
			return &stepPreparationFailure{
				kind: FailureKindContract, code: "execution.output.invalid", cause: validateOutputErr,
			}
		}
	}
	digest, err := executionStateDigest(p.lastStableState)
	if err != nil {
		return &stepPreparationFailure{
			kind: FailureKindContract, code: "engine.last_stable.invalid", cause: err,
		}
	}
	sequence := p.committedSteps + 1
	wire := preparedStepWire{
		StepSequence: sequence, LastStableDigest: digest, CandidateState: result.candidateState,
		SignalCursor: p.mailbox.committedSignalCursor() + uint64(transition.ConsumedSignals()),
		Transition:   transition,
	}
	for index, effect := range transition.Effects() {
		wire.Effects = append(wire.Effects, preparedEffectWire{
			ID: deriveEffectID(p.controller.processID, sequence, index), Effect: effect,
			Phase: effectPhasePlanned,
		})
	}
	p.prepared = &preparedStep{wire: wire, candidate: result.candidate}
	p.usage.PreparedEffects += effectCount
	p.updateView()
	p.publishEvent(ctx, EventStepPrepared, EventPhaseAttempt, sequence, EffectID{}, emptyEventPayload())
	return nil
}

func (p *processState) finalizePrepared(ctx context.Context) error {
	finalization, err := newPreparedStepFinalization(p)
	if err != nil {
		return err
	}
	defer finalization.rollback()
	if err := finalization.prepare(); err != nil {
		return err
	}
	return finalization.commit(ctx)
}

type preparedStepFinalization struct {
	loop                  *processState
	prepared              *preparedStep
	mailbox               signalMailbox
	consumedChildWaits    []WaitID
	registeredChildWaits  []WaitID
	immediateChildSignals []Signal
	transition            preparedTransitionState
	committed             bool
}

type preparedTransitionState struct {
	status           Status
	currentWaitID    WaitID
	pauseReason      string
	finalOutput      Output
	termination      Termination
	finishedAt       time.Time
	closedChildWaits []WaitID
}

func newPreparedStepFinalization(loop *processState) (*preparedStepFinalization, error) {
	mailbox := loop.mailbox.clone()
	consumedChildWaits, err := mailbox.commit(loop.prepared.wire.Transition.ConsumedSignals())
	if err != nil {
		return nil, err
	}
	return &preparedStepFinalization{
		loop: loop, prepared: loop.prepared, mailbox: mailbox,
		consumedChildWaits: consumedChildWaits,
	}, nil
}

func (p *preparedStepFinalization) prepare() error {
	for _, record := range p.prepared.wire.Effects {
		if err := p.applySettlement(record); err != nil {
			return err
		}
	}
	if err := p.enqueueImmediateChildSignals(); err != nil {
		return err
	}
	return p.prepareTransition()
}

func (p *preparedStepFinalization) applySettlement(record preparedEffectWire) error {
	if !record.definitelySettled() {
		return errors.New("effect batch is not definitely settled")
	}
	var waitID WaitID
	if record.WaitID != nil {
		waitID = *record.WaitID
	}
	signal, err := newSignal(deriveSettlementSignalID(record.ID), waitID, record.Settlement.Payload())
	if err != nil {
		return err
	}
	if record.Effect.Target() == EffectTargetFramework {
		operation, operationErr := decodeFrameworkEffectOperation(record.Effect.Payload())
		if operationErr != nil {
			return errors.New("invalid prepared framework Effect")
		}
		switch operation {
		case frameworkEffectWait:
			key, _, decodeErr := decodeWaitRequest(record.Effect)
			if decodeErr != nil {
				return decodeErr
			}
			return p.mailbox.openWait(key, signal, true)
		case frameworkEffectWaitChildren:
			return p.registerChildWait(record, signal)
		case frameworkEffectStartChild:
			if waitID.Valid() {
				return errors.New("child-start Effect unexpectedly contains a WaitID")
			}
		default:
			return errors.New("unsupported prepared framework Effect")
		}
	}
	accepted, err := p.mailbox.enqueue(StatusRunning, signal, signalSourceExternal)
	if err != nil || !accepted {
		return errors.Join(err, errors.New("internal settlement Signal was not accepted"))
	}
	return nil
}

func (p *preparedStepFinalization) registerChildWait(record preparedEffectWire, signal Signal) error {
	spec, err := decodeChildWaitEffect(record.Effect.Payload())
	if err != nil || record.WaitID == nil {
		return errors.New("invalid child-wait Effect")
	}
	waitID := *record.WaitID
	if openErr := p.mailbox.openWait(spec.Key, signal, false); openErr != nil {
		return openErr
	}
	if p.loop.runtime == nil {
		return ErrInvalidChildWait
	}
	immediateSignal, immediatelySatisfied, err := p.loop.runtime.registerChildWait(
		p.loop.controller.processID, waitID, spec,
	)
	if err != nil {
		return err
	}
	p.registeredChildWaits = append(p.registeredChildWaits, waitID)
	if immediatelySatisfied {
		p.immediateChildSignals = append(p.immediateChildSignals, immediateSignal)
	}
	return nil
}

func (p *preparedStepFinalization) enqueueImmediateChildSignals() error {
	preparedSignals := uint64(len(p.prepared.wire.Effects))
	reservedBudget := p.loop.effectiveReservedBudget()
	for index, signal := range p.immediateChildSignals {
		acceptedSignals := uint64(index) + 1
		if !resourceQuantitiesFit(
			p.loop.limits.MaxSignals,
			p.loop.usage.AcceptedSignals, preparedSignals, acceptedSignals,
		) || !resourceQuantitiesFit(
			p.loop.limits.MaxPendingSignals, p.mailbox.pendingCount(), 1,
		) || !resourceQuantitiesFit(
			p.loop.budget.Signals,
			p.loop.usage.AcceptedSignals, reservedBudget.Signals,
			preparedSignals, acceptedSignals,
		) {
			return ErrResourceLimitExceeded
		}
		accepted, err := p.mailbox.enqueue(StatusRunning, signal, signalSourceChildCompletion)
		if err != nil || !accepted {
			return errors.Join(err, errors.New("immediate child completion Signal was not accepted"))
		}
	}
	return nil
}

func (p *preparedStepFinalization) prepareTransition() error {
	transition := p.prepared.wire.Transition
	switch transition.Kind() {
	case TransitionKindContinue:
		p.transition.status = StatusRunning
	case TransitionKindWait:
		return p.prepareWaitTransition(transition)
	case TransitionKindPause:
		p.transition.status = StatusPaused
		p.transition.pauseReason, _ = transition.Reason()
	case TransitionKindComplete:
		output, _ := transition.Output()
		p.prepareTermination(completedOutcome())
		if p.transition.status == StatusCompleted {
			p.transition.finalOutput = output
		}
	case TransitionKindFail:
		failure, _ := transition.Failure()
		outcome, err := failedOutcome(failure)
		if err != nil {
			return err
		}
		p.prepareTermination(outcome)
	default:
		return ErrInvalidTransition
	}
	return nil
}

func (p *preparedStepFinalization) prepareWaitTransition(transition Transition) error {
	waitID, _ := transition.WaitID()
	shouldWait, err := p.mailbox.enterWait(waitID)
	if err != nil {
		return err
	}
	if shouldWait {
		p.transition.status = StatusWaiting
		p.transition.currentWaitID = waitID
	} else {
		p.transition.status = StatusRunning
	}
	return nil
}

func (p *preparedStepFinalization) prepareTermination(outcome stepOutcome) {
	p.transition.termination = p.loop.resolveStepTermination(outcome)
	p.transition.status = p.transition.termination.Status()
	p.transition.finishedAt = time.Now().Round(0).UTC()
	p.transition.closedChildWaits = p.mailbox.closeAllWaits()
}

func (p *preparedStepFinalization) commit(ctx context.Context) error {
	execution := p.prepared.candidate
	if execution == nil {
		var err error
		execution, err = restoreExecution(
			p.loop.deployment.Definition(), p.prepared.wire.CandidateState,
		)
		if err != nil {
			return err
		}
	}
	loop := p.loop
	loop.execution = execution
	loop.lastStableState = p.prepared.wire.CandidateState
	loop.mailbox = p.mailbox
	loop.committedSteps = p.prepared.wire.StepSequence
	loop.usage.CommittedSteps = loop.committedSteps
	loop.usage.AcceptedSignals += uint64(len(p.prepared.wire.Effects))
	loop.usage.AcceptedSignals += uint64(len(p.immediateChildSignals))
	loop.prepared = nil
	if p.transition.termination.Valid() {
		loop.installTermination(p.transition.termination, p.transition.finalOutput, p.transition.finishedAt)
	} else {
		loop.status = p.transition.status
		loop.currentWaitID = p.transition.currentWaitID
		loop.pauseReason = p.transition.pauseReason
	}
	loop.updateView()
	payload, _ := json.Marshal(stepCommittedEventPayload{ProcessStatus: loop.status})
	loop.publishEvent(ctx, EventStepCommitted, EventPhaseCommitted, loop.committedSteps, EffectID{}, payload)
	if loop.status == StatusPaused {
		loop.publishEventAfterCheckpoint(
			ctx, EventProcessPaused, EventPhaseCommitted, 0, EffectID{}, emptyEventPayload(),
		)
	}
	for _, waitID := range p.consumedChildWaits {
		loop.runtime.unregisterChildWait(waitID)
	}
	for _, waitID := range p.transition.closedChildWaits {
		loop.runtime.unregisterChildWait(waitID)
	}
	p.committed = true
	return nil
}

func (p *preparedStepFinalization) rollback() {
	if p == nil || p.committed {
		return
	}
	for _, waitID := range p.registeredChildWaits {
		p.loop.runtime.unregisterChildWait(waitID)
	}
}

func (p *processState) discardPrepared() {
	p.prepared = nil
	p.discardExecution()
}

func (p *processState) discardExecution() {
	execution, err := restoreExecution(p.deployment.Definition(), p.lastStableState)
	if err == nil {
		p.execution = execution
	} else {
		p.execution = nil
	}
}

// Asynchronous failures wait for accepted external effects to settle before
// becoming terminal, just like cancellation and deadline intents.
func (p *processState) recordFailure(kind FailureKind, code string, err error) {
	if p.pendingControl.failure.Valid() {
		return
	}
	failure, failureErr := failureFromError(kind, code, err)
	if failureErr != nil {
		failure, _ = NewFailure(FailureKindContract, "engine.failure.invalid", "Engine could not construct a valid failure")
	}
	p.pendingControl.failure = failure
}

func (p *processState) fail(kind FailureKind, code string, err error) {
	p.recordFailure(kind, code, err)
	p.commitTermination(stepOutcome{})
}

func (p *processState) commitTermination(outcome stepOutcome) {
	p.commitTerminationWithUnresolved(outcome, nil)
}

func (p *processState) commitTerminationWithUnresolved(
	outcome stepOutcome,
	unresolvedEffectIDs []EffectID,
) {
	termination := p.resolveStepTermination(outcome)
	p.installTermination(termination.withUnresolvedEffectIDs(unresolvedEffectIDs), Output{}, time.Now().Round(0).UTC())
	for _, waitID := range p.mailbox.closeAllWaits() {
		p.runtime.unregisterChildWait(waitID)
	}
	p.updateView()
}

func (p *processState) installTermination(termination Termination, output Output, finishedAt time.Time) {
	p.termination = termination
	p.status = termination.Status()
	p.finishedAt = finishedAt
	p.currentWaitID = WaitID{}
	p.pauseReason = ""
	p.pendingControl = pendingControl{}
	p.finalOutput = Output{}
	if p.status == StatusCompleted {
		p.finalOutput = output
	}
}

func (p *processState) resolveStepTermination(outcome stepOutcome) Termination {
	if p.pendingControl.failure.Valid() {
		outcome, _ = failedOutcome(p.pendingControl.failure)
	}
	termination, err := resolveTermination(terminationFacts{
		kill: p.pendingControl.kill, deadline: p.pendingControl.deadline,
		cancellation: p.pendingControl.cancellation, outcome: outcome,
	})
	if err != nil {
		failure, _ := NewFailure(FailureKindContract, "engine.termination.invalid", err.Error())
		termination = terminationForFailure(failure)
	}
	return termination
}
