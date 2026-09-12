package agent

import (
	"errors"
	"time"
)

type stepPreparationFailure struct {
	kind  FailureKind
	code  string
	cause error
}

type preparedStepFinalization struct {
	process               *processState
	prepared              *preparedStep
	mailbox               signalMailbox
	consumedChildWaits    []WaitID
	openedChildWaits      []ChildWaitOpened
	immediateChildSignals []Signal
	transition            preparedTransitionState
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
	consumedChildWaits, err := mailbox.commit(process.prepared.Transition.ConsumedSignals())
	if err != nil {
		return nil, err
	}
	return &preparedStepFinalization{
		process: process, prepared: process.prepared, mailbox: mailbox,
		consumedChildWaits: consumedChildWaits,
	}, nil
}

func (p *preparedStepFinalization) prepareSettlements() error {
	for _, record := range p.prepared.Effects {
		if err := p.applySettlement(record); err != nil {
			return err
		}
	}
	return nil
}

func (p *preparedStepFinalization) applySettlement(record preparedEffect) error {
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
			return p.openChildWait(record, signal)
		case frameworkEffectStartChild, frameworkEffectSignalChild, frameworkEffectCancelChild:
			if waitID.Valid() {
				return errors.New("child operation unexpectedly contains a WaitID")
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

func (p *preparedStepFinalization) openChildWait(record preparedEffect, signal Signal) error {
	spec, err := decodeChildWaitEffect(record.Effect.Payload())
	if err != nil || record.WaitID == nil {
		return errors.New("invalid child-wait Effect")
	}
	if err := p.mailbox.openWait(spec.Key, signal, false); err != nil {
		return err
	}
	p.openedChildWaits = append(p.openedChildWaits, ChildWaitOpened{waitID: *record.WaitID, spec: spec})
	return nil
}

func (p *preparedStepFinalization) enqueueImmediateChildSignals() error {
	preparedSignals := p.prepared.settlementSignalCount()
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
		accepted, err := p.mailbox.enqueue(StatusRunning, signal, signalSourceChildWait)
		if err != nil || !accepted {
			return errors.Join(err, errors.New("immediate child completion Signal was not accepted"))
		}
	}
	return nil
}

func (p *preparedStepFinalization) prepareTransition(finishedAt time.Time) error {
	transition := p.prepared.Transition
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
		p.prepareTermination(completedOutcome(), finishedAt)
		if p.transition.status == StatusCompleted {
			p.transition.finalOutput = output
		}
	case TransitionKindFail:
		failure, _ := transition.Failure()
		outcome, err := failedOutcome(failure)
		if err != nil {
			return err
		}
		p.prepareTermination(outcome, finishedAt)
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

func (p *preparedStepFinalization) prepareTermination(outcome stepOutcome, finishedAt time.Time) {
	p.transition.termination = p.process.resolveStepTermination(outcome)
	p.transition.status = p.transition.termination.Status()
	p.transition.finishedAt = finishedAt
	p.transition.closedChildWaits = p.mailbox.closeAllWaits()
}

func (p *preparedStepFinalization) adopt() {
	process := p.process
	process.execution = process.preparedExecution
	process.preparedExecution = nil
	process.committedExecutionState = p.prepared.CandidateState
	process.mailbox = p.mailbox
	process.committedSteps = p.prepared.StepSequence
	process.usage.CommittedSteps = process.committedSteps
	process.usage.AcceptedSignals += p.prepared.settlementSignalCount()
	process.usage.AcceptedSignals += uint64(len(p.immediateChildSignals))
	process.prepared = nil
	if p.transition.termination.Valid() {
		process.installTermination(p.transition.termination, p.transition.finalOutput, p.transition.finishedAt)
	} else {
		process.status = p.transition.status
		process.currentWaitID = p.transition.currentWaitID
		process.pauseReason = p.transition.pauseReason
	}
}
