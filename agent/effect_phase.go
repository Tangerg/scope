package agent

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"

	"github.com/Tangerg/scope/agent/internal/jsonwire"
)

// effectPhase stays private: callers act through typed boundaries, not by
// moving the kernel's program counter.
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

// EffectID follows from the enclosing Process, Step sequence, and batch position.
// Progress exists once dispatch is permitted; its settlement owns completion.
// Local wait results follow from the request and are reconstructed on decoding.
type preparedEffect struct {
	ID       EffectID
	Effect   Effect
	progress *effectProgress
}

type effectProgress struct {
	settlement *Settlement
	diagnostic *Failure
}

func (e *effectProgress) clone() *effectProgress {
	if e == nil {
		return nil
	}
	clone := *e
	if e.settlement != nil {
		settlement := *e.settlement
		clone.settlement = &settlement
	}
	if e.diagnostic != nil {
		diagnostic := *e.diagnostic
		clone.diagnostic = &diagnostic
	}
	return &clone
}

func (p preparedEffect) phase() effectPhase {
	if p.progress == nil {
		return effectPhasePlanned
	}
	if p.progress.settlement == nil {
		return effectPhasePending
	}
	return effectPhaseSettled
}

func (p preparedEffect) settlement() *Settlement {
	if p.progress == nil {
		return nil
	}
	return p.progress.settlement
}

func (p preparedEffect) diagnostic() *Failure {
	if p.progress == nil {
		return nil
	}
	return p.progress.diagnostic
}

type preparedEffectWire struct {
	Effect   Effect              `json:"effect"`
	Progress *effectProgressWire `json:"progress,omitzero"`
}

type effectProgressWire struct {
	Settlement *preparedSettlementWire `json:"settlement,omitzero"`
	Diagnostic *Failure                `json:"diagnostic,omitzero"`
}

type preparedSettlementWire struct {
	Status  SettlementStatus `json:"status,omitzero"`
	Payload json.RawMessage  `json:"payload,omitzero"`
}

func (p preparedEffect) MarshalJSON() ([]byte, error) {
	wire := preparedEffectWire{Effect: p.Effect}
	_, local, err := p.localOutcome()
	if err != nil {
		return nil, err
	}
	if p.progress != nil {
		wire.Progress = &effectProgressWire{Diagnostic: p.diagnostic()}
		if settlement := p.settlement(); settlement != nil {
			wire.Progress.Settlement = &preparedSettlementWire{}
			if !local {
				wire.Progress.Settlement.Status = settlement.status
				wire.Progress.Settlement.Payload = settlement.payload
			}
		}
	}
	return jsonv2.Marshal(wire)
}

// UnmarshalJSON leaves the record unbound until bindIDs supplies its identity.
func (p *preparedEffect) UnmarshalJSON(data []byte) error {
	wire, err := jsonwire.Decode[preparedEffectWire](data, "effect")
	if err != nil {
		return err
	}
	record := preparedEffect{Effect: wire.Effect}
	outcome, local, err := record.localOutcome()
	if err != nil {
		return err
	}
	if wire.Progress != nil {
		record.progress = &effectProgress{diagnostic: wire.Progress.Diagnostic}
		if settlement := wire.Progress.Settlement; settlement != nil {
			if local {
				if settlement.Status != SettlementStatusInvalid || settlement.Payload != nil {
					return errors.New("prepared Effect stores the settlement its request determines")
				}
				record.progress.settlement = &Settlement{status: SettlementStatusSucceeded, payload: outcome}
			} else {
				if !settlement.Status.Valid() {
					return fmt.Errorf("%w: status is required", ErrInvalidSettlement)
				}
				payload, err := normalizeJSON(settlement.Payload, MaxPayloadBytes)
				if err != nil {
					return fmt.Errorf("%w: payload: %w", ErrInvalidSettlement, err)
				}
				record.progress.settlement = &Settlement{status: settlement.Status, payload: payload}
			}
		}
	}
	*p = record
	return nil
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

// bindIDs derives each record's EffectID from its batch position.
func (p preparedEffects) bindIDs(processID ProcessID, sequence uint64) {
	for index := range p {
		id := processID.effectID(sequence, index)
		p[index].ID = id
		if p[index].settlement() != nil {
			p[index].settlement().effectID = id
		}
	}
}

func (p preparedEffects) next() (int, error) {
	next, found := len(p), false
	for index, record := range p {
		if err := record.validatePhase(); err != nil {
			return 0, err
		}
		if found {
			if record.phase() != effectPhasePlanned {
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
	if diagnostic := p.diagnostic(); diagnostic != nil && (!diagnostic.Valid() || p.settlement() == nil) {
		return errors.New("invalid effect diagnostic")
	}
	if settlement := p.settlement(); settlement != nil && !settlement.Valid() {
		return errors.New("prepared Effect settlement is invalid")
	}
	return nil
}

func (p *preparedEffect) begin() error {
	if p == nil || p.phase() != effectPhasePlanned {
		return errors.New("effect is not planned")
	}
	p.progress = &effectProgress{}
	return nil
}

// Size projections use the same settlement envelopes as live execution. Local
// wait results are known before dispatch; an admitted child operation must fit
// its bounded refusal, and uncertain dispatch must retain its diagnostic.
// The return value accounts for reserved text omitted from the compact projection.
func (p preparedEffect) snapshotReservation() (preparedEffect, uint64, error) {
	p.progress = p.progress.clone()
	if p.settlement() != nil {
		return p, 0, nil
	}
	failure := snapshotReservationFailure()
	if p.Effect.Target() == EffectTargetDispatcher {
		if p.phase() == effectPhasePending {
			if err := p.settleUnknown(); err != nil {
				return p, 0, err
			}
			p.progress.diagnostic = &failure
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
	if p == nil || p.phase() != effectPhasePending {
		return errors.New("only a pending dispatch permission can be revoked")
	}
	p.progress = nil
	return nil
}

func (p *preparedEffect) settle(settlement Settlement, cause error) error {
	if p == nil || p.phase() != effectPhasePending {
		return errors.New("effect is not pending")
	}
	if !settlement.Valid() {
		return errors.New("incoming settlement is invalid")
	}
	if settlement.EffectID() != p.ID {
		return errors.New("incoming settlement identifies another Effect")
	}
	p.progress.settlement = &settlement
	if cause != nil {
		diagnostic := dispatchFailure(cause)
		p.progress.diagnostic = &diagnostic
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
	settlement, err := NewSettlement(p.ID, result.settlementStatus(), payload)
	if err != nil {
		return err
	}
	return p.settle(settlement, nil)
}

func (p *preparedEffect) resolveUnknown(settlement Settlement) error {
	if p == nil || p.settlement() == nil {
		return errors.New("effect has no settled outcome")
	}
	if p.settlement().Status() != SettlementStatusUnknown {
		return errors.New("effect outcome is already definite")
	}
	if !settlement.Valid() || settlement.Status() == SettlementStatusUnknown {
		return errors.New("resolution must supply a definite settlement")
	}
	if settlement.EffectID() != p.ID {
		return errors.New("resolution identifies another Effect")
	}
	p.progress.settlement = &settlement
	return nil
}

func (p *preparedEffect) unknown() bool {
	return p.settlement() != nil &&
		p.settlement().Status() == SettlementStatusUnknown
}

func (p *preparedEffect) definitelySettled() bool {
	return p.settlement() != nil &&
		p.settlement().Status() != SettlementStatusUnknown
}

func (p *preparedEffect) validateEffect() error {
	if !p.Effect.Valid() {
		return errors.New("prepared Effect payload is invalid")
	}
	if p.Effect.Target() != EffectTargetFramework {
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
	settlement, err := NewSettlement(p.ID, result.settlementStatus(), payload)
	if err != nil {
		return err
	}
	return p.settle(settlement, nil)
}

// settleLocally succeeds with the outcome operation determines by itself.
func (p *preparedEffect) settleLocally(operation frameworkOperation) error {
	payload, _, err := operation.localOutcome()
	if err != nil {
		return err
	}
	settlement, err := NewSettlement(p.ID, SettlementStatusSucceeded, payload)
	if err != nil {
		return err
	}
	return p.settle(settlement, nil)
}

// localOutcome reports the settlement payload a framework operation fixes
// without an external answer, which the wire therefore never stores.
func (p preparedEffect) localOutcome() (json.RawMessage, bool, error) {
	if p.Effect.Target() != EffectTargetFramework {
		return nil, false, nil
	}
	operation, err := decodeFrameworkOperation(p.Effect.Payload())
	if err != nil {
		return nil, false, err
	}
	return operation.localOutcome()
}

// settlementSignal delivers the settled outcome to the Execution. Only a wait
// opening addresses the wait its Effect identity derives.
func (p *preparedEffect) settlementSignal(waitID WaitID) (Signal, error) {
	return NewSignal(p.ID.settlementSignalID(), waitID, p.settlement().Payload())
}
