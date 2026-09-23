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
	process            *processState
	prepared           *preparedStep
	mailbox            signalMailbox
	consumedChildWaits []WaitID
	openedChildWaits   []ChildWaitOpened
	commit             preparedStepCommit
}

type preparedStepCommit struct {
	status           Status
	currentWaitID    WaitID
	pauseReason      string
	finalOutput      Payload
	termination      Termination
	finishedAt       time.Time
	closedChildWaits []WaitID
}

func newPreparedStepFinalization(process *processState, prepared *preparedStep) (*preparedStepFinalization, error) {
	mailbox := process.mailbox.clone()
	consumedChildWaits, err := mailbox.commit(prepared.Intent.ConsumedSignals())
	if err != nil {
		return nil, err
	}
	return &preparedStepFinalization{
		process: process, prepared: prepared, mailbox: mailbox,
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
	signal, err := NewSignal(record.ID.settlementSignalID(), waitID, record.Settlement.Payload())
	if err != nil {
		return err
	}
	if record.Effect.Target() == EffectTargetFramework {
		operation, err := decodeFrameworkOperation(record.Effect.Payload())
		if err != nil {
			return err
		}
		return operation.apply(p, record, signal)
	}
	return p.enqueueSettlement(signal)
}

func (p *preparedStepFinalization) enqueueSettlement(signal Signal) error {
	accepted, err := p.mailbox.enqueue(StatusRunning, signal, signalSourceSettlement)
	if err != nil || !accepted {
		return errors.Join(err, errors.New("internal settlement Signal was not accepted"))
	}
	return nil
}

func (p *preparedStepFinalization) prepareTransition(finishedAt time.Time) error {
	transition := p.prepared.Intent
	switch transition.Kind() {
	case TransitionKindContinue, TransitionKindCheckpoint:
		p.commit.status = StatusRunning
	case TransitionKindWait:
		return p.prepareWaitTransition(transition)
	case TransitionKindPause:
		p.commit.status = StatusPaused
		p.commit.pauseReason, _ = transition.Reason()
	case TransitionKindComplete:
		output, _ := transition.Output()
		p.prepareTermination(completedOutcome(), finishedAt)
		if p.commit.status == StatusCompleted {
			p.commit.finalOutput = output
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
		p.commit.status = StatusWaiting
		p.commit.currentWaitID = waitID
	} else {
		p.commit.status = StatusRunning
	}
	return nil
}

func (p *preparedStepFinalization) prepareTermination(outcome stepOutcome, finishedAt time.Time) {
	p.commit.termination = p.process.resolveStepTermination(outcome)
	p.commit.status = p.commit.termination.Status()
	p.commit.finishedAt = finishedAt
	p.commit.closedChildWaits = p.mailbox.closeAllWaits()
}
