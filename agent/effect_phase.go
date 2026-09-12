package agent

import (
	"encoding/json"
	"errors"
	"fmt"
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

type preparedEffect struct {
	ID         EffectID    `json:"id"`
	Effect     Effect      `json:"effect"`
	Phase      effectPhase `json:"phase"`
	WaitID     *WaitID     `json:"wait_id,omitempty"`
	Settlement *Settlement `json:"settlement,omitempty"`
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

func (p preparedEffect) validatePhase() error {
	if !p.Phase.valid() || (p.Phase == effectPhaseSettled) != (p.Settlement != nil) ||
		p.Settlement != nil && (!p.Settlement.Valid() || p.Settlement.EffectID() != p.ID) {
		return errors.New("prepared Effect phase and settlement disagree")
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

// A pending boundary grants dispatch permission before I/O starts. Only the
// owning incarnation can revoke an unused permission; recovery cannot prove it
// was unused and must retain an uncertain outcome instead.
func (p *preparedEffect) revokeDispatch() {
	if p.Phase != effectPhasePending {
		panic("agent: only a pending dispatch permission can be revoked")
	}
	p.Phase = effectPhasePlanned
}

func (p *preparedEffect) settle(settlement Settlement) error {
	if p == nil || p.Phase != effectPhasePending || p.Settlement != nil ||
		!settlement.Valid() || settlement.EffectID() != p.ID {
		return errors.New("effect is not pending or settlement does not match")
	}
	p.Phase = effectPhaseSettled
	p.Settlement = &settlement
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
	return p.settle(settlement)
}

func (p *preparedEffect) resolveUnknown(settlement Settlement) error {
	if p == nil || p.Phase != effectPhaseSettled || p.Settlement == nil ||
		p.Settlement.Status() != SettlementStatusUnknown ||
		!settlement.Valid() || settlement.Status() == SettlementStatusUnknown ||
		settlement.EffectID() != p.ID {
		return errors.New("effect is not settled as unknown or resolution does not match")
	}
	p.Settlement = &settlement
	return nil
}

func (p preparedEffect) unknown() bool {
	return p.Phase == effectPhaseSettled && p.Settlement != nil &&
		p.Settlement.Status() == SettlementStatusUnknown
}

func (p preparedEffect) definitelySettled() bool {
	return p.Phase == effectPhaseSettled && p.Settlement != nil &&
		p.Settlement.Status() != SettlementStatusUnknown
}

func (p preparedEffect) validateIdentity(
	processID ProcessID,
	sequence uint64,
	index int,
	effect Effect,
) error {
	wantID := processID.effectID(sequence, index)
	if p.ID != wantID || !p.Effect.equal(effect) {
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

func (p preparedEffect) validateFramework() error {
	operation, err := decodeFrameworkEffectOperation(p.Effect.Payload())
	if err != nil {
		return err
	}
	switch operation {
	case frameworkEffectWait:
		return p.validateWait("wait Effect")
	case frameworkEffectStartChild:
		if p.WaitID != nil ||
			p.Settlement != nil && p.Settlement.Status() == SettlementStatusUnknown {
			return errors.New("child-start Effect has an invalid settlement")
		}
		return nil
	case frameworkEffectWaitChildren:
		return p.validateWait("child-wait Effect")
	case frameworkEffectSignalChild, frameworkEffectCancelChild:
		return p.validateChildControl()
	default:
		return errors.New("unsupported framework Effect")
	}
}

func (p preparedEffect) validateChildControl() error {
	if p.WaitID != nil {
		return ErrInvalidChildControl
	}
	if p.Settlement == nil {
		return nil
	}
	request, err := decodeChildControlEffect(p.Effect.Payload())
	if err != nil {
		return err
	}
	result, err := decodeChildControlResult(p.Settlement.Payload())
	if err != nil || result.childID != request.ChildID || result.operation != request.Operation {
		return ErrInvalidChildControl
	}
	wantStatus := SettlementStatusSucceeded
	if result.failure.Valid() {
		wantStatus = SettlementStatusFailed
	}
	if p.Settlement.Status() != wantStatus || !result.Matches(p.Effect) {
		return ErrInvalidChildControl
	}
	return nil
}

func (p preparedEffect) validateWait(name string) error {
	if p.WaitID != nil && *p.WaitID != p.ID.waitID() {
		return fmt.Errorf("%s contains a non-derived WaitID", name)
	}
	if (p.WaitID == nil) != (p.Phase != effectPhaseSettled) ||
		p.Settlement != nil && p.Settlement.Status() == SettlementStatusUnknown {
		return fmt.Errorf("%s has an incomplete or unknown settlement", name)
	}
	return nil
}

func (p *preparedEffect) settleFramework() error {
	operation, err := decodeFrameworkEffectOperation(p.Effect.Payload())
	if err != nil {
		return err
	}
	var payload json.RawMessage
	switch operation {
	case frameworkEffectWait:
		_, payload, err = p.Effect.waitRequest()
		if err != nil {
			return err
		}
	case frameworkEffectStartChild:
		// Child start crosses admission and initialization boundaries. treeRuntime
		// intercepts it and commits its fenced job completion atomically.
		return fmt.Errorf("%w: child start requires its job outcome", ErrInvalidEffect)
	case frameworkEffectWaitChildren:
		spec, decodeErr := decodeChildWaitEffect(p.Effect.Payload())
		if decodeErr != nil {
			return decodeErr
		}
		payload, err = encodeChildWaitOpened(spec)
		if err != nil {
			return err
		}
	default:
		return fmt.Errorf("%w: unsupported local Framework Effect", ErrInvalidEffect)
	}
	settlement, err := NewSettlement(p.ID, SettlementStatusSucceeded, payload)
	if err != nil {
		return err
	}
	if err := p.settle(settlement); err != nil {
		return err
	}
	waitID := p.ID.waitID()
	p.WaitID = &waitID
	return nil
}

func (p *preparedEffect) settleChildStart(result ChildStartResult) error {
	payload, err := encodeChildStartResult(result)
	if err != nil {
		return p.settleUnknown()
	}
	status := SettlementStatusSucceeded
	if _, failed := result.Failure(); failed {
		status = SettlementStatusFailed
	}
	settlement, err := NewSettlement(p.ID, status, payload)
	if err != nil {
		return p.settleUnknown()
	}
	return p.settle(settlement)
}
