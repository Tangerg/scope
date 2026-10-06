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

// The candidate mailbox owns every wait the Step opens, consumes, or closes.
type preparedStepFinalization struct {
	process  *processState
	prepared *preparedStep
	mailbox  signalMailbox
	commit   preparedStepCommit
}

// openedChildWaits lists the child waits this Step's batch opens, which may
// already be satisfied. Each opening's WaitID derives from its Effect.
func (p *preparedStepFinalization) openedChildWaits() ([]openedChildWait, error) {
	var opened []openedChildWait
	for _, record := range p.prepared.Effects {
		if record.Effect.Target() != EffectTargetFramework {
			continue
		}
		operation, err := decodeFrameworkOperation(record.Effect.Payload())
		if err != nil {
			return nil, err
		}
		if wait, opens := operation.(childWaitOperation); opens {
			opened = append(opened, openedChildWait{waitID: record.ID.waitID(), spec: wait.spec})
		}
	}
	return opened, nil
}

// preparedStepCommit holds only what finalization decides beyond the
// prepared Intent: the termination it resolves and when it finished. The
// Intent keeps owning the wait it enters, the pause it requests, and its
// Output.
type preparedStepCommit struct {
	termination Termination
	finishedAt  time.Time
}

func newPreparedStepFinalization(process *processState, prepared *preparedStep) (*preparedStepFinalization, error) {
	mailbox := process.mailbox.clone()
	if err := mailbox.commit(prepared.Intent.ConsumedSignals()); err != nil {
		return nil, err
	}
	return &preparedStepFinalization{process: process, prepared: prepared, mailbox: mailbox}, nil
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
	signal, err := record.settlementSignal()
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
	case TransitionKindComplete:
		p.prepareTermination(completedOutcome(), finishedAt)
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
	return p.mailbox.enterWait(waitID)
}

// finalOutput is the Intent's Output only when the resolved termination still
// completes; a higher-priority termination supersedes it.
func (p *preparedStepFinalization) finalOutput() Payload {
	if p.commit.termination.Status() != StatusCompleted {
		return Payload{}
	}
	output, _ := p.prepared.Intent.Output()
	return output
}

func (p *preparedStepFinalization) prepareTermination(outcome stepOutcome, finishedAt time.Time) {
	p.commit.termination = p.process.resolveStepTermination(outcome)
	p.commit.finishedAt = finishedAt
}
