package agent

import (
	"encoding/json"
	"errors"
)

// effectPhase is the durable lifecycle of one Effect in a prepared batch.
// It stays private because callers act through typed boundaries, not by
// mutating the kernel's program counter.
type effectPhase string

const (
	effectPhasePlanned effectPhase = "planned"
	effectPhasePending effectPhase = "pending"
	effectPhaseSettled effectPhase = "settled"
)

func (e effectPhase) valid() bool {
	switch e {
	case effectPhasePlanned, effectPhasePending, effectPhaseSettled:
		return true
	default:
		return false
	}
}

func (e effectPhase) String() string { return string(e) }

// preparedEffects owns the sequential execution frontier. An uncertain result
// occupies the frontier until adjudication, just as a pending Effect does.
type preparedEffects []preparedEffectWire

func (p preparedEffects) unknownEffectIDs() []EffectID {
	var ids []EffectID
	for _, effect := range p {
		if effect.unknown() {
			ids = append(ids, effect.ID)
		}
	}
	return ids
}

func (p preparedEffects) next() (int, error) {
	next := len(p)
	for index, record := range p {
		if err := record.validatePhase(); err != nil {
			return 0, err
		}
		if next != len(p) {
			if record.Phase != effectPhasePlanned {
				return 0, errors.New("started Effect follows an incomplete Effect")
			}
			continue
		}
		if !record.definitelySettled() {
			next = index
		}
	}
	return next, nil
}

func (p preparedEffectWire) validatePhase() error {
	if !p.Phase.valid() || (p.Phase == effectPhaseSettled) != (p.Settlement != nil) ||
		p.Settlement != nil && (!p.Settlement.Valid() || p.Settlement.EffectID() != p.ID) {
		return errors.New("prepared Effect phase and settlement disagree")
	}
	return nil
}

func (p *preparedEffectWire) begin() error {
	if p == nil || p.Phase != effectPhasePlanned || p.Settlement != nil {
		return errors.New("effect is not planned")
	}
	p.Phase = effectPhasePending
	return nil
}

func (p *preparedEffectWire) settle(settlement Settlement) error {
	if p == nil || p.Phase != effectPhasePending || p.Settlement != nil ||
		!settlement.Valid() || settlement.EffectID() != p.ID {
		return errors.New("effect is not pending or settlement does not match")
	}
	p.Phase = effectPhaseSettled
	p.Settlement = &settlement
	return nil
}

func (p *preparedEffectWire) settleUnknown() error {
	if p == nil || !p.ID.Valid() {
		return errors.New("effect identity is invalid")
	}
	settlement, err := NewSettlement(
		p.ID, SettlementStatusUnknown, json.RawMessage(nullJSON),
	)
	if err != nil {
		return err
	}
	return p.settle(settlement)
}

func (p *preparedEffectWire) resolveUnknown(settlement Settlement) error {
	if p == nil || p.Phase != effectPhaseSettled || p.Settlement == nil ||
		p.Settlement.Status() != SettlementStatusUnknown ||
		!settlement.Valid() || settlement.Status() == SettlementStatusUnknown ||
		settlement.EffectID() != p.ID {
		return errors.New("effect is not settled as unknown or resolution does not match")
	}
	p.Settlement = &settlement
	return nil
}

func (p preparedEffectWire) unknown() bool {
	return p.Phase == effectPhaseSettled && p.Settlement != nil &&
		p.Settlement.Status() == SettlementStatusUnknown
}

func (p preparedEffectWire) definitelySettled() bool {
	return p.Phase == effectPhaseSettled && p.Settlement != nil &&
		p.Settlement.Status() != SettlementStatusUnknown
}

func (p *preparedStep) settleUnknown(effectID EffectID) error {
	if p == nil {
		return errors.New("prepared Step is missing")
	}
	for index := range p.wire.Effects {
		record := &p.wire.Effects[index]
		if record.ID == effectID && record.Phase == effectPhasePending {
			return record.settleUnknown()
		}
	}
	return errors.New("pending Effect is missing")
}
