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

func (e effectPhase) String() string {
	if !e.valid() {
		return invalidEnumName
	}
	return string(e)
}

// preparedEffect is mutable owner-local state; readers and mutators share its identity.
type preparedEffect struct {
	ID         EffectID    `json:"id"`
	Effect     Effect      `json:"effect"`
	Phase      effectPhase `json:"phase"`
	WaitID     *WaitID     `json:"wait_id,omitzero"`
	Settlement *Settlement `json:"settlement,omitzero"`
	Diagnostic *Failure    `json:"diagnostic,omitzero"`
}

// preparedEffects owns the sequential execution frontier. An uncertain result
// occupies the frontier until adjudication, just as a pending Effect does.
type preparedEffects []preparedEffect

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
	next, found := len(p), false
	for index, record := range p {
		if err := record.validatePhase(); err != nil {
			return 0, err
		}
		if found {
			if record.Phase != effectPhasePlanned {
				return 0, errors.New("started Effect follows an incomplete Effect")
			}
			continue
		}
		if !record.definitelySettled() {
			next, found = index, true
		}
	}
	return next, nil
}

func (p *preparedEffect) validatePhase() error {
	if p.Diagnostic != nil && (!p.Diagnostic.Valid() || p.Phase != effectPhaseSettled) {
		return errors.New("invalid effect diagnostic")
	}
	if !p.Phase.valid() {
		return errors.New("prepared Effect phase is invalid")
	}
	if (p.Phase == effectPhaseSettled) != (p.Settlement != nil) {
		return errors.New("prepared Effect settlement presence disagrees with phase")
	}
	if p.Settlement != nil {
		if !p.Settlement.Valid() {
			return errors.New("prepared Effect settlement is invalid")
		}
		if p.Settlement.EffectID() != p.ID {
			return errors.New("prepared Effect settlement identifies another Effect")
		}
	}
	return nil
}

func (p *preparedEffect) begin() error {
	if p == nil || p.Phase != effectPhasePlanned || p.Settlement != nil {
		return errors.New("effect is not planned")
	}
	p.Phase = effectPhasePending
	return nil
}

// Size projections use the same settlement envelopes as live execution. Local
// wait results are known before dispatch; an admitted child operation must fit
// its bounded refusal, and uncertain dispatch must retain its diagnostic.
// The return value accounts for reserved text omitted from the compact projection.
func (p preparedEffect) snapshotReservation() (preparedEffect, uint64, error) {
	if p.Settlement != nil {
		return p, 0, nil
	}
	failure := Failure{kind: FailureKindExecution, code: snapshotReservationText, message: snapshotReservationText}
	if p.Effect.Target() == EffectTargetDispatcher {
		if p.Phase == effectPhasePending {
			if err := p.settleUnknown(); err != nil {
				return p, 0, err
			}
			p.Diagnostic = &failure
			return p, snapshotFailureGrowth, nil
		}
		return p, 0, nil
	}
	operation, err := decodeFrameworkOperation(p.Effect.Payload())
	if err != nil {
		return p, 0, err
	}
	growth, err := operation.reserve(&p, failure)
	return p, growth, err
}

// A pending boundary grants dispatch permission before I/O starts. Only the
// owning incarnation can revoke an unused permission; recovery cannot prove it
// was unused and must retain an uncertain outcome instead.
func (p *preparedEffect) revokeDispatch() error {
	if p == nil || p.Phase != effectPhasePending || p.Settlement != nil {
		return errors.New("only a pending dispatch permission can be revoked")
	}
	p.Phase = effectPhasePlanned
	return nil
}

func (p *preparedEffect) settle(settlement Settlement, cause error) error {
	if p == nil || p.Phase != effectPhasePending {
		return errors.New("effect is not pending")
	}
	if p.Settlement != nil {
		return errors.New("pending Effect already has a settlement")
	}
	if !settlement.Valid() {
		return errors.New("incoming settlement is invalid")
	}
	if settlement.EffectID() != p.ID {
		return errors.New("incoming settlement identifies another Effect")
	}
	p.Phase = effectPhaseSettled
	p.Settlement = &settlement
	if cause != nil {
		diagnostic := dispatchFailure(cause)
		p.Diagnostic = &diagnostic
	}
	return nil
}

func (p *preparedEffect) settleUnknown() error {
	if p == nil || !p.ID.Valid() {
		return errors.New("effect identity is invalid")
	}
	settlement, err := NewSettlement(
		p.ID, SettlementStatusUnknown, json.RawMessage(nullJSON),
	)
	if err != nil {
		return err
	}
	return p.settle(settlement, nil)
}

func (p *preparedEffect) settleChildControl(result ChildControlResult) error {
	payload, err := result.MarshalJSON()
	if err != nil {
		return err
	}
	status := SettlementStatusSucceeded
	if result.failure.Valid() {
		status = SettlementStatusFailed
	}
	settlement, err := NewSettlement(p.ID, status, payload)
	if err != nil {
		return err
	}
	return p.settle(settlement, nil)
}

func (p *preparedEffect) resolveUnknown(settlement Settlement) error {
	if p == nil || p.Phase != effectPhaseSettled || p.Settlement == nil {
		return errors.New("effect has no settled outcome")
	}
	if p.Settlement.Status() != SettlementStatusUnknown {
		return errors.New("effect outcome is already definite")
	}
	if !settlement.Valid() || settlement.Status() == SettlementStatusUnknown {
		return errors.New("resolution must supply a definite settlement")
	}
	if settlement.EffectID() != p.ID {
		return errors.New("resolution identifies another Effect")
	}
	p.Settlement = &settlement
	return nil
}

func (p *preparedEffect) unknown() bool {
	return p.Phase == effectPhaseSettled && p.Settlement != nil &&
		p.Settlement.Status() == SettlementStatusUnknown
}

func (p *preparedEffect) definitelySettled() bool {
	return p.Phase == effectPhaseSettled && p.Settlement != nil &&
		p.Settlement.Status() != SettlementStatusUnknown
}

func (p *preparedEffect) validateIdentity(
	processID ProcessID,
	sequence uint64,
	index int,
) error {
	wantID := processID.effectID(sequence, index)
	if p.ID != wantID || !p.Effect.Valid() {
		return errors.New("prepared Effect identity or payload changed")
	}
	if p.Effect.Target() != EffectTargetFramework {
		if p.WaitID != nil {
			return errors.New("dispatcher Effect cannot contain WaitID")
		}
		return nil
	}
	return p.validateFramework()
}

func (p *preparedEffect) validateFramework() error {
	operation, err := decodeFrameworkOperation(p.Effect.Payload())
	if err != nil {
		return err
	}
	return operation.validate(p)
}

func (p *preparedEffect) validateWait() error {
	if p.WaitID != nil && *p.WaitID != p.ID.waitID() {
		return errors.New("wait Effect contains a non-derived WaitID")
	}
	if (p.WaitID == nil) != (p.Phase != effectPhaseSettled) ||
		p.Settlement != nil && p.Settlement.Status() == SettlementStatusUnknown {
		return errors.New("wait Effect has an incomplete or unknown settlement")
	}
	if p.Settlement == nil {
		return nil
	}
	if p.Settlement.Status() != SettlementStatusSucceeded {
		return errors.New("wait Effect settlement is not successful")
	}
	return nil
}

func (p *preparedEffect) settleFramework() error {
	operation, err := decodeFrameworkOperation(p.Effect.Payload())
	if err != nil {
		return err
	}
	return operation.settle(p)
}

func (p *preparedEffect) settleChildStart(result ChildStartResult) error {
	payload, err := result.MarshalJSON()
	if err != nil {
		return err
	}
	status := SettlementStatusSucceeded
	if _, failed := result.Failure(); failed {
		status = SettlementStatusFailed
	}
	settlement, err := NewSettlement(p.ID, status, payload)
	if err != nil {
		return err
	}
	return p.settle(settlement, nil)
}

func (p *preparedEffect) settleWait(payload json.RawMessage) error {
	settlement, err := NewSettlement(p.ID, SettlementStatusSucceeded, payload)
	if err != nil {
		return err
	}
	if err := p.settle(settlement, nil); err != nil {
		return err
	}
	waitID := p.ID.waitID()
	p.WaitID = &waitID
	return nil
}
