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
	if !transition.Valid() || uint64(transition.ConsumedSignals()) > result.deliveredSignals {
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
	digest, err := executionStateDigest(p.committedExecutionState)
	if err != nil {
		return &stepPreparationFailure{
			kind: FailureKindContract, code: "engine.committed_execution_state.invalid", cause: err,
		}
	}
	sequence := p.committedSteps + 1
	wire := preparedStepWire{
		StepSequence: sequence, CommittedExecutionStateDigest: digest, CandidateState: result.candidateState,
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
	finalization.commit(ctx)
	return nil
}

type preparedStepFinalization struct {
	process               *processState
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

func newPreparedStepFinalization(process *processState) (*preparedStepFinalization, error) {
	mailbox := process.mailbox.clone()
	consumedChildWaits, err := mailbox.commit(process.prepared.wire.Transition.ConsumedSignals())
	if err != nil {
		return nil, err
	}
	return &preparedStepFinalization{
		process: process, prepared: process.prepared, mailbox: mailbox,
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
	if p.process.runtime == nil {
		return ErrInvalidChildWait
	}
	immediateSignal, immediatelySatisfied, err := p.process.runtime.registerChildWait(
		p.process.controller.processID, waitID, spec,
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
	reservedBudget := p.process.effectiveReservedBudget()
	for index, signal := range p.immediateChildSignals {
		acceptedSignals := uint64(index) + 1
		if !resourceQuantitiesFit(
			p.process.limits.MaxSignals,
			p.process.usage.AcceptedSignals, preparedSignals, acceptedSignals,
		) || !resourceQuantitiesFit(
			p.process.limits.MaxPendingSignals, p.mailbox.pendingCount(), 1,
		) || !resourceQuantitiesFit(
			p.process.budget.Signals,
			p.process.usage.AcceptedSignals, reservedBudget.Signals,
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
	p.transition.termination = p.process.resolveStepTermination(outcome)
	p.transition.status = p.transition.termination.Status()
	p.transition.finishedAt = time.Now().Round(0).UTC()
	p.transition.closedChildWaits = p.mailbox.closeAllWaits()
}

func (p *preparedStepFinalization) commit(ctx context.Context) {
	process := p.process
	process.execution = p.prepared.candidate
	process.committedExecutionState = p.prepared.wire.CandidateState
	process.mailbox = p.mailbox
	process.committedSteps = p.prepared.wire.StepSequence
	process.usage.CommittedSteps = process.committedSteps
	process.usage.AcceptedSignals += uint64(len(p.prepared.wire.Effects))
	process.usage.AcceptedSignals += uint64(len(p.immediateChildSignals))
	process.prepared = nil
	if p.transition.termination.Valid() {
		process.installTermination(p.transition.termination, p.transition.finalOutput, p.transition.finishedAt)
	} else {
		process.status = p.transition.status
		process.currentWaitID = p.transition.currentWaitID
		process.pauseReason = p.transition.pauseReason
	}
	process.updateView()
	payload, _ := json.Marshal(stepCommittedEventPayload{ProcessStatus: process.status})
	process.publishEvent(ctx, EventStepCommitted, EventPhaseCommitted, process.committedSteps, EffectID{}, payload)
	if process.status == StatusPaused {
		process.publishEventAfterCheckpoint(
			ctx, EventProcessPaused, EventPhaseCommitted, 0, EffectID{}, emptyEventPayload(),
		)
	}
	for _, waitID := range p.consumedChildWaits {
		process.runtime.unregisterChildWait(waitID)
	}
	for _, waitID := range p.transition.closedChildWaits {
		process.runtime.unregisterChildWait(waitID)
	}
	p.committed = true
}

func (p *preparedStepFinalization) rollback() {
	if p == nil || p.committed {
		return
	}
	for _, waitID := range p.registeredChildWaits {
		p.process.runtime.unregisterChildWait(waitID)
	}
}

func (p *processState) discardPrepared() {
	p.prepared = nil
	p.discardExecution()
}

func (p *processState) discardExecution() {
	execution, err := restoreExecution(p.deployment.Definition(), p.committedExecutionState)
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
	p.pendingControl.failure = newEngineFailure(kind, code, err)
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
