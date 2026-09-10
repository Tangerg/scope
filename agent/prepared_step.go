package agent

import "errors"

// preparedStep owns candidate state and the effect lifecycle until adoption.
// Its fields are the single persisted representation. Executable instances
// belong to processState so portable facts cannot carry runtime authority.
type preparedStep struct {
	StepSequence                  uint64          `json:"step_sequence"`
	CommittedExecutionStateDigest Digest          `json:"committed_execution_state_digest"`
	CandidateState                ExecutionState  `json:"candidate_state"`
	SignalCursor                  uint64          `json:"signal_cursor"`
	Transition                    Transition      `json:"transition"`
	Effects                       preparedEffects `json:"effects,omitempty"`
}

// nextEffect returns the execution frontier after checking the entire batch.
// A nil record means that every Effect is definitely settled.
func (p *preparedStep) nextEffect() (int, *preparedEffect, error) {
	index, err := p.Effects.next()
	if err != nil || index == len(p.Effects) {
		return index, nil, err
	}
	return index, &p.Effects[index], nil
}

func (p *preparedStep) pendingEffect(id EffectID) (int, *preparedEffect) {
	if p != nil {
		for index := range p.Effects {
			record := &p.Effects[index]
			if record.ID == id && record.Phase == effectPhasePending {
				return index, record
			}
		}
	}
	return 0, nil
}

// Both live and durable resolution use this transition before publication.
func (p *preparedStep) resolveUnknown(settlement Settlement) (int, *preparedEffect, error) {
	if p == nil || !settlement.Valid() || settlement.Status() == SettlementStatusUnknown {
		return 0, nil, ErrEffectNotPending
	}
	for index := range p.Effects {
		record := &p.Effects[index]
		if record.ID == settlement.EffectID() {
			if err := record.resolveUnknown(settlement); err != nil {
				return 0, nil, ErrEffectNotPending
			}
			return index, record, nil
		}
	}
	return 0, nil, ErrEffectNotPending
}

func (p *preparedStep) settlementSignalCount() uint64 {
	if p == nil {
		return 0
	}
	return uint64(len(p.Effects))
}

func (p *preparedStep) consumedSignals() uint64 {
	if p == nil {
		return 0
	}
	return uint64(p.Transition.ConsumedSignals())
}

func (p *preparedStep) hasUnknownSettlement() bool {
	for _, effect := range p.Effects {
		if effect.unknown() {
			return true
		}
	}
	return false
}

func (p preparedStep) validate(processID ProcessID, sequence uint64, committedState ExecutionState, mailbox signalMailbox) error {
	if p.StepSequence != sequence || !p.CandidateState.Valid() || !p.Transition.Valid() ||
		p.SignalCursor < mailbox.committedSignalCursor() || p.SignalCursor > mailbox.arrivalSequence() {
		return errors.New("invalid prepared Step boundary")
	}
	digest, err := executionStateDigest(committedState)
	if err != nil || digest != p.CommittedExecutionStateDigest {
		return errors.New("prepared Step does not identify committed Execution state")
	}
	if p.SignalCursor != mailbox.committedSignalCursor()+p.consumedSignals() {
		return errors.New("prepared Step consumption does not match Transition")
	}
	effects := p.Transition.Effects()
	if len(effects) != len(p.Effects) {
		return errors.New("prepared Effect count does not match Transition")
	}
	for index, record := range p.Effects {
		if effectErr := record.validateIdentity(processID, sequence, index, effects[index]); effectErr != nil {
			return effectErr
		}
	}
	_, err = p.Effects.next()
	return err
}

func (p preparedStep) snapshot() preparedStep {
	clone := p
	clone.Effects = make([]preparedEffect, len(p.Effects))
	for index, effect := range p.Effects {
		clone.Effects[index] = preparedEffect{
			ID: effect.ID, Effect: effect.Effect.clone(), Phase: effect.Phase,
		}
		if effect.WaitID != nil {
			waitID := *effect.WaitID
			clone.Effects[index].WaitID = &waitID
		}
		if effect.Settlement != nil {
			settlement := *effect.Settlement
			clone.Effects[index].Settlement = &settlement
		}
	}
	return clone
}
