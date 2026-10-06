package agent

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
)

// The closed protocol binds decoding, preparation, execution and recovery for
// each framework operation. Consumers cannot choose different interpretations
// of the same payload at different lifecycle boundaries.
type frameworkOperation interface {
	// settlement builds the only settlement the request admits once execution
	// adds its sole fact: an optional definite failure. Live settling,
	// projections, and decoding all use it, so no stored copy can disagree.
	settlement(id EffectID, failure Failure) (Settlement, error)
	// settledFailure recovers that failure from a settlement built above.
	settledFailure(Settlement) (Failure, error)
	reserve(*preparedEffect, Failure) (uint64, error)
	apply(*preparedStepFinalization, preparedEffect) error
	validateTree(*treeSnapshotValidation, ProcessID, preparedEffect) error
}

// The header deliberately accepts operation-owned fields; the selected strict
// decoder below owns their validation.
type frameworkOperationHeader struct {
	Operation frameworkOperationKind `json:"operation"`
}

func decodeFrameworkOperation(payload json.RawMessage) (frameworkOperation, error) {
	var header frameworkOperationHeader
	if err := jsonv2.Unmarshal(payload, &header); err != nil {
		return nil, fmt.Errorf("%w: decode Framework Effect header: %w", ErrInvalidEffect, err)
	}
	switch header.Operation {
	case frameworkOperationWait:
		key, err := decodeWaitRequestPayload(payload)
		return waitOperation{key: key}, err
	case frameworkOperationWaitChildren:
		spec, err := decodeChildWaitEffect(payload)
		return childWaitOperation{spec: spec}, err
	case frameworkOperationStartChild:
		spec, err := decodeChildStartEffect(payload)
		return childStartOperation{spec: spec}, err
	case frameworkOperationSignalChild, frameworkOperationCancelChild:
		request, err := decodeChildControlEffect(payload)
		return childControlOperation{request: request}, err
	default:
		return nil, fmt.Errorf("%w: unsupported Framework Effect", ErrInvalidEffect)
	}
}

type waitOperation struct{ key WaitKey }

// A wait settles locally the moment it begins, so it never fails.
func (waitOperation) settlement(_ EffectID, failure Failure) (Settlement, error) {
	if failure.Valid() {
		return Settlement{}, errors.New("a wait Effect cannot fail")
	}
	return NewSettlement(SettlementStatusSucceeded, waitOpenedPayload())
}

func (waitOperation) settledFailure(Settlement) (Failure, error) { return Failure{}, nil }

func (w waitOperation) reserve(effect *preparedEffect, _ Failure) (uint64, error) {
	return 0, effect.settleLocally(w)
}

func (w waitOperation) apply(finalization *preparedStepFinalization, record preparedEffect) error {
	signal, err := record.openingSignal()
	if err != nil {
		return err
	}
	return finalization.mailbox.openWait(w.key, signal)
}

func (w waitOperation) validateTree(*treeSnapshotValidation, ProcessID, preparedEffect) error {
	return nil
}

type childWaitOperation struct{ spec ChildWaitSpec }

// A child wait settles locally the moment it begins, so it never fails.
func (childWaitOperation) settlement(_ EffectID, failure Failure) (Settlement, error) {
	if failure.Valid() {
		return Settlement{}, errors.New("a child-wait Effect cannot fail")
	}
	return NewSettlement(SettlementStatusSucceeded, childWaitOpenedPayload())
}

func (childWaitOperation) settledFailure(Settlement) (Failure, error) { return Failure{}, nil }

func (c childWaitOperation) reserve(effect *preparedEffect, _ Failure) (uint64, error) {
	return 0, effect.settleLocally(c)
}

func (c childWaitOperation) apply(finalization *preparedStepFinalization, record preparedEffect) error {
	signal, err := record.openingSignal()
	if err != nil {
		return err
	}
	if err := finalization.mailbox.openChildWait(c.spec, signal); err != nil {
		return err
	}
	finalization.openedChildWaits = append(finalization.openedChildWaits, openedChildWait{waitID: signal.waitID, spec: c.spec})
	return nil
}

func (c childWaitOperation) validateTree(t *treeSnapshotValidation, parent ProcessID, _ preparedEffect) error {
	if t.processes[parent].terminal() {
		return nil
	}
	return c.spec.validateRelations(parent, t.processRelation)
}

type childStartOperation struct{ spec ChildSpec }

// A started child's identity follows from the Effect.
func (c childStartOperation) settlement(id EffectID, failure Failure) (Settlement, error) {
	result := ChildStartResult{failure: failure}
	if !failure.Valid() {
		result.processID = id.childProcessID()
	}
	payload, err := result.MarshalJSON()
	if err != nil {
		return Settlement{}, err
	}
	return NewSettlement(result.settlementStatus(), payload)
}

func (childStartOperation) settledFailure(settlement Settlement) (Failure, error) {
	result, err := decodeChildStartResult(settlement.payload)
	return result.failure, err
}

func (c childStartOperation) reserve(effect *preparedEffect, failure Failure) (uint64, error) {
	if effect.phase() != effectPhasePending {
		return 0, nil
	}
	return snapshotFailureGrowth, effect.settleOperation(c, failure)
}

func (c childStartOperation) apply(finalization *preparedStepFinalization, record preparedEffect) error {
	signal, err := record.settlementSignal()
	if err != nil {
		return err
	}
	return finalization.enqueueSettlement(signal)
}

// A started child's request decodes from the child's own record, so a
// successful start needs only that the child is captured; an unsettled start
// never has its child in the tree, because the child publishes atomically with
// its start settlement.
func (c childStartOperation) validateTree(t *treeSnapshotValidation, _ ProcessID, record preparedEffect) error {
	_, exists := t.processes[record.ID.childProcessID()]
	if !record.definitelySettled() {
		if exists {
			return fmt.Errorf("%w: child exists before its start settled", ErrInvalidChildStart)
		}
		return nil
	}
	result, err := decodeChildStartResult(record.settlement().Payload())
	if err != nil {
		return err
	}
	if _, started := result.ProcessID(); started && !exists {
		return fmt.Errorf("%w: started child is missing", ErrInvalidChildStart)
	}
	return nil
}

type childControlOperation struct{ request childControlEffectWire }

// The request fixes the recipient, operation, and delivered SignalID.
func (childControlOperation) settlement(_ EffectID, failure Failure) (Settlement, error) {
	result := ChildControlResult{failure: failure}
	payload, err := result.MarshalJSON()
	if err != nil {
		return Settlement{}, err
	}
	return NewSettlement(result.settlementStatus(), payload)
}

func (childControlOperation) settledFailure(settlement Settlement) (Failure, error) {
	result, err := decodeChildControlResult(settlement.payload)
	return result.failure, err
}

func (c childControlOperation) reserve(effect *preparedEffect, failure Failure) (uint64, error) {
	if effect.phase() != effectPhasePending {
		return 0, nil
	}
	return snapshotFailureGrowth, effect.settleOperation(c, failure)
}

func (c childControlOperation) apply(finalization *preparedStepFinalization, record preparedEffect) error {
	signal, err := record.settlementSignal()
	if err != nil {
		return err
	}
	return finalization.enqueueSettlement(signal)
}

func (c childControlOperation) validateTree(t *treeSnapshotValidation, parent ProcessID, record preparedEffect) error {
	if !record.definitelySettled() {
		return nil
	}
	result, err := decodeChildControlResult(record.settlement().Payload())
	if err != nil || result.failure.Valid() {
		return err
	}
	child, present := t.processes[c.request.ChildID]
	if actualParent, _ := child.Relation.ParentID(); !present || actualParent != parent {
		return ErrInvalidChildControl
	}
	if c.request.Operation == frameworkOperationCancelChild {
		if !child.terminal() && child.PendingControl.Cancellation == nil {
			return ErrInvalidChildControl
		}
		return nil
	}
	for _, receipt := range child.Mailbox.receipts() {
		if receipt.ID() != c.request.Signal.ID() {
			continue
		}
		if !receipt.Matches(*c.request.Signal) {
			return ErrInvalidChildControl
		}
		return nil
	}
	return ErrInvalidChildControl
}
