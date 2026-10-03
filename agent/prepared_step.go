package agent

import (
	"errors"
)

// preparedStep owns candidate state and the effect lifecycle until adoption.
// Its fields are the single persisted representation. Executable instances
// belong to processState so portable facts cannot carry runtime authority.
// Intent owns prospective Signal consumption; only adoption advances the mailbox.
// The step always follows the committed one it would replace, so its sequence,
// base state, and Effect identities belong to the enclosing Process record.
type preparedStep struct {
	CandidateState ExecutionState
	Intent         Transition
	Effects        preparedEffects
}

// preparedStepWire is decoded only by the Process record that encloses it,
// because that record's identity and committed progress number every Effect.
type preparedStepWire struct {
	CandidateState ExecutionState       `json:"candidate_state"`
	Intent         Transition           `json:"intent"`
	Effects        []preparedEffectWire `json:"effects,omitempty"`
}

func (p preparedStep) wire() (preparedStepWire, error) {
	wire := preparedStepWire{CandidateState: p.CandidateState, Intent: p.Intent}
	for _, record := range p.Effects {
		effect, err := record.wire()
		if err != nil {
			return preparedStepWire{}, err
		}
		wire.Effects = append(wire.Effects, effect)
	}
	return wire, nil
}

// step rebuilds the prepared Step numbered sequence of processID.
func (p preparedStepWire) step(processID ProcessID, sequence uint64) (preparedStep, error) {
	step := preparedStep{CandidateState: p.CandidateState, Intent: p.Intent}
	for index, wire := range p.Effects {
		record, err := wire.record(processID.effectID(sequence, index))
		if err != nil {
			return preparedStep{}, err
		}
		step.Effects = append(step.Effects, record)
	}
	return step, nil
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
			if record.ID == id && record.phase() == effectPhasePending {
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
	return uint64(p.Intent.ConsumedSignals())
}

func (p *preparedStep) hasUnknownSettlement() bool {
	if p == nil {
		return false
	}
	for _, effect := range p.Effects {
		if effect.unknown() {
			return true
		}
	}
	return false
}

func (p *preparedStep) validate(mailbox signalMailbox) error {
	if !p.CandidateState.Valid() {
		return errors.New("prepared Step candidate state is invalid")
	}
	if !p.Intent.Valid() {
		return errors.New("prepared Step transition is invalid")
	}
	if p.consumedSignals() > mailbox.pendingCount() {
		return errors.New("prepared Step consumption exceeds pending Signals")
	}
	if len(p.Intent.effects) != 0 || len(p.Effects) != 0 && p.Intent.Kind() != TransitionKindContinue {
		return errors.New("prepared Effects must belong only to the execution records of a continue intent")
	}
	for _, record := range p.Effects {
		if effectErr := record.validateEffect(); effectErr != nil {
			return effectErr
		}
	}
	_, err := p.Effects.next()
	return err
}

func (p *preparedStep) clone() preparedStep {
	clone := *p
	clone.Effects = make([]preparedEffect, len(p.Effects))
	for index, effect := range p.Effects {
		clone.Effects[index] = preparedEffect{
			ID: effect.ID, Effect: effect.Effect.clone(), progress: effect.progress.clone(),
		}
	}
	return clone
}
