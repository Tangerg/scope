package agent

import (
	"errors"
	"time"
)

type stepFailure struct {
	kind  FailureKind
	code  string
	cause error
}

// newFinalizationFailure attributes an exhausted bound to limitCode; any other
// finalization error is a contract violation of the prepared Step.
func newFinalizationFailure(limitCode string, err error) *stepFailure {
	if errors.Is(err, ErrResourceLimitExceeded) {
		return &stepFailure{kind: FailureKindExecution, code: limitCode, cause: err}
	}
	return &stepFailure{kind: FailureKindContract, code: failureCodeEngineFinalizeInvalid, cause: err}
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
	currentWaitID    WaitID
	pause            pause
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
	if record.Effect.Target() == EffectTargetFramework {
		operation, err := decodeFrameworkOperation(record.Effect.Payload())
		if err != nil {
			return err
		}
		return operation.apply(p, record)
	}
	signal, err := record.settlementSignal(WaitID{})
	if err != nil {
		return err
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
		return nil
	case TransitionKindWait:
		return p.prepareWaitTransition(transition)
	case TransitionKindPause:
		p.commit.pause = transition.pause
	case TransitionKindComplete:
		output, _ := transition.Output()
		p.prepareTermination(completedOutcome(), finishedAt)
		if p.commit.termination.Status() == StatusCompleted {
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
		p.commit.currentWaitID = waitID
	}
	return nil
}

func (p *preparedStepFinalization) prepareTermination(outcome stepOutcome, finishedAt time.Time) {
	p.commit.termination = p.process.resolveStepTermination(outcome)
	p.commit.finishedAt = finishedAt
	p.commit.closedChildWaits = p.mailbox.closeAllWaits()
}
