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

// preparedEffect's identity follows from its Process, Step sequence, and batch
// position, and a wait's settlement follows from its request. Memory keeps
// both on the record; the wire omits them, decoding derives the settlement,
// and bindIDs restores the identities.
type preparedEffect struct {
	ID         EffectID
	Effect     Effect
	Phase      effectPhase
	Settlement *Settlement
	Diagnostic *Failure
}

type preparedEffectWire struct {
	Effect     Effect                  `json:"effect"`
	Phase      effectPhase             `json:"phase"`
	Settlement *preparedSettlementWire `json:"settlement,omitzero"`
	Diagnostic *Failure                `json:"diagnostic,omitzero"`
}

type preparedSettlementWire struct {
	Status  SettlementStatus `json:"status"`
	Payload json.RawMessage  `json:"payload"`
}

func (p preparedEffect) MarshalJSON() ([]byte, error) {
	wire := preparedEffectWire{Effect: p.Effect, Phase: p.Phase, Diagnostic: p.Diagnostic}
	_, local, err := p.localOutcome()
	if err != nil {
		return nil, err
	}
	if p.Settlement != nil && !local {
		wire.Settlement = &preparedSettlementWire{Status: p.Settlement.status, Payload: p.Settlement.payload}
	}
	return jsonv2.Marshal(wire)
}

// UnmarshalJSON leaves the record unbound until bindIDs supplies its identity.
func (p *preparedEffect) UnmarshalJSON(data []byte) error {
	wire, err := jsonwire.Decode[preparedEffectWire](data, "effect", "phase")
	if err != nil {
		return err
	}
	record := preparedEffect{Effect: wire.Effect, Phase: wire.Phase, Diagnostic: wire.Diagnostic}
	outcome, local, err := record.localOutcome()
	if err != nil {
		return err
	}
	if local {
		if wire.Settlement != nil {
			return errors.New("prepared Effect stores the settlement its request determines")
		}
		if record.Phase == effectPhaseSettled {
			record.Settlement = &Settlement{status: SettlementStatusSucceeded, payload: outcome}
		}
	} else if wire.Settlement != nil {
		if !wire.Settlement.Status.Valid() {
			return fmt.Errorf("%w: status is required", ErrInvalidSettlement)
		}
		payload, err := normalizeJSON(wire.Settlement.Payload, MaxPayloadBytes)
		if err != nil {
			return fmt.Errorf("%w: payload: %w", ErrInvalidSettlement, err)
		}
		record.Settlement = &Settlement{status: wire.Settlement.Status, payload: payload}
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
		if p[index].Settlement != nil {
			p[index].Settlement.effectID = id
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
	if p.Settlement == nil {
		return nil
	}
	if !p.Settlement.Valid() {
		return errors.New("prepared Effect settlement is invalid")
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
	failure := snapshotReservationFailure()
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
	settlement, err := NewSettlement(p.ID, result.settlementStatus(), payload)
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
	return NewSignal(p.ID.settlementSignalID(), waitID, p.Settlement.Payload())
}
