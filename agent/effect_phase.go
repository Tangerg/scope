package agent

import (
	"encoding/json"
	"errors"

	"github.com/samber/lo"
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

// preparedEffectWire omits what the record's position and request determine:
// its EffectID and, for a Framework Effect, every settlement fact except an
// optional failure. A child start that published its child keeps only the
// input: the child's record owns the key, Deployment, budget, and
// capabilities it was granted, so the request decodes from that record.
type preparedEffectWire struct {
	Effect       *Effect             `json:"effect,omitzero"`
	StartedChild *startedChildWire   `json:"started_child,omitzero"`
	Progress     *effectProgressWire `json:"progress,omitzero"`
}

type startedChildWire struct {
	Input Payload `json:"input"`
}

// childGrantSource returns the ChildSpec, without input, that a published
// child's record was granted.
type childGrantSource func(child ProcessID) (ChildSpec, error)

type effectProgressWire struct {
	Settlement *preparedSettlementWire `json:"settlement,omitzero"`
	Diagnostic *Failure                `json:"diagnostic,omitzero"`
}

// A Dispatcher settlement keeps its status and payload. A Framework
// settlement keeps only the failure its request cannot determine.
type preparedSettlementWire struct {
	Status  SettlementStatus `json:"status,omitzero"`
	Payload json.RawMessage  `json:"payload,omitzero"`
	Failure *Failure         `json:"failure,omitzero"`
}

func (p preparedEffect) wire() (preparedEffectWire, error) {
	wire := preparedEffectWire{Effect: new(p.Effect)}
	if p.progress == nil {
		return wire, nil
	}
	wire.Progress = &effectProgressWire{Diagnostic: p.diagnostic()}
	settlement := p.settlement()
	if settlement == nil {
		return wire, nil
	}
	if p.Effect.Target() != EffectTargetFramework {
		wire.Progress.Settlement = &preparedSettlementWire{Status: settlement.status, Payload: settlement.payload}
		return wire, nil
	}
	operation, err := decodeFrameworkOperation(p.Effect.payload)
	if err != nil {
		return preparedEffectWire{}, err
	}
	failure, err := operation.settledFailure(*settlement)
	if err != nil {
		return preparedEffectWire{}, err
	}
	wire.Progress.Settlement = &preparedSettlementWire{}
	if failure.Valid() {
		wire.Progress.Settlement.Failure = &failure
		return wire, nil
	}
	if start, starts := operation.(childStartOperation); starts {
		wire.Effect, wire.StartedChild = nil, &startedChildWire{Input: start.spec.Input}
	}
	return wire, nil
}

// record rebuilds the prepared Effect at id, deriving a Framework settlement
// from its request and retained failure, and a started child's request from
// the grant its child's record owns.
func (p preparedEffectWire) record(id EffectID, grants childGrantSource) (preparedEffect, error) {
	effect, err := p.effect(id, grants)
	if err != nil {
		return preparedEffect{}, err
	}
	record := preparedEffect{ID: id, Effect: effect}
	if p.Progress == nil {
		return record, nil
	}
	record.progress = &effectProgress{diagnostic: p.Progress.Diagnostic}
	stored := p.Progress.Settlement
	if stored == nil {
		return record, nil
	}
	var settlement Settlement
	if effect.Target() != EffectTargetFramework {
		if stored.Failure != nil {
			return preparedEffect{}, errors.New("prepared Dispatcher settlement stores a Framework failure")
		}
		var err error
		if settlement, err = NewSettlement(stored.Status, stored.Payload); err != nil {
			return preparedEffect{}, err
		}
	} else {
		if stored.Status != SettlementStatusInvalid || stored.Payload != nil {
			return preparedEffect{}, errors.New("prepared Framework settlement stores what its request determines")
		}
		operation, err := decodeFrameworkOperation(effect.payload)
		if err != nil {
			return preparedEffect{}, err
		}
		if _, starts := operation.(childStartOperation); starts && p.StartedChild == nil && stored.Failure == nil {
			return preparedEffect{}, errors.New("started child request stores the grant its child owns")
		}
		if settlement, err = operation.settlement(id, lo.FromPtr(stored.Failure)); err != nil {
			return preparedEffect{}, err
		}
	}
	record.progress.settlement = &settlement
	return record, nil
}

func (p preparedEffectWire) effect(id EffectID, grants childGrantSource) (Effect, error) {
	switch {
	case (p.Effect == nil) == (p.StartedChild == nil):
		return Effect{}, errors.New("prepared Effect needs exactly one request or started child")
	case p.Effect != nil:
		return *p.Effect, nil
	}
	settlement := lo.FromPtr(p.Progress).Settlement
	if settlement == nil || settlement.Failure != nil {
		return Effect{}, errors.New("started child requires its successful settlement")
	}
	spec, err := grants(id.childProcessID())
	if err != nil {
		return Effect{}, err
	}
	spec.Input = p.StartedChild.Input
	return NewChildStartEffect(spec)
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
	settlement, err := NewSettlement(SettlementStatusUnknown, json.RawMessage(nullJSON))
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
	return nil
}

// settleOperation settles a Framework Effect with the only fact its execution
// adds to the request.
func (p *preparedEffect) settleOperation(operation frameworkOperation, failure Failure) error {
	settlement, err := operation.settlement(p.ID, failure)
	if err != nil {
		return err
	}
	return p.settle(settlement, nil)
}

// settleLocally begins and succeeds a wait, whose request fixes its outcome.
func (p *preparedEffect) settleLocally(operation frameworkOperation) error {
	if p.phase() == effectPhasePlanned {
		if err := p.begin(); err != nil {
			return err
		}
	}
	return p.settleOperation(operation, Failure{})
}

// settlementSignal delivers the settled outcome to the Execution. A
// Dispatcher settlement carries its status; a Framework operation's payload
// already owns its outcome.
func (p *preparedEffect) settlementSignal() (Signal, error) {
	if p.Effect.Target() == EffectTargetDispatcher {
		return NewSettlementSignal(p.ID.settlementSignalID(), *p.settlement())
	}
	return NewSignal(p.ID.settlementSignalID(), WaitID{}, p.settlement().Payload())
}

// openingSignal acknowledges the wait the Effect's identity derives; the
// WaitID it addresses also names the acknowledgement.
func (p *preparedEffect) openingSignal() (Signal, error) {
	waitID := p.ID.waitID()
	return NewSignal(waitID.openingSignalID(), waitID, p.settlement().Payload())
}
