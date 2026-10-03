package agent

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"slices"
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
		key, signal, err := decodeWaitRequestPayload(payload)
		return waitOperation{key: key, payload: signal}, err
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

type waitOperation struct {
	key     WaitKey
	payload json.RawMessage
}

// A wait settles locally the moment it begins, so it never fails.
func (w waitOperation) settlement(id EffectID, failure Failure) (Settlement, error) {
	if failure.Valid() {
		return Settlement{}, errors.New("a wait Effect cannot fail")
	}
	return NewSettlement(id, SettlementStatusSucceeded, w.payload)
}

func (waitOperation) settledFailure(Settlement) (Failure, error) { return Failure{}, nil }

func (w waitOperation) reserve(effect *preparedEffect, _ Failure) (uint64, error) {
	return 0, effect.settleLocally(w)
}

func (w waitOperation) apply(finalization *preparedStepFinalization, record preparedEffect) error {
	signal, err := record.settlementSignal(record.ID.waitID())
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
func (c childWaitOperation) settlement(id EffectID, failure Failure) (Settlement, error) {
	if failure.Valid() {
		return Settlement{}, errors.New("a child-wait Effect cannot fail")
	}
	payload, err := childWaitOpenedPayload(c.spec)
	if err != nil {
		return Settlement{}, err
	}
	return NewSettlement(id, SettlementStatusSucceeded, payload)
}

func (childWaitOperation) settledFailure(Settlement) (Failure, error) { return Failure{}, nil }

func (c childWaitOperation) reserve(effect *preparedEffect, _ Failure) (uint64, error) {
	return 0, effect.settleLocally(c)
}

func (c childWaitOperation) apply(finalization *preparedStepFinalization, record preparedEffect) error {
	waitID := record.ID.waitID()
	signal, err := record.settlementSignal(waitID)
	if err != nil {
		return err
	}
	if err := finalization.mailbox.openChildWait(c.spec, signal); err != nil {
		return err
	}
	finalization.openedChildWaits = append(finalization.openedChildWaits, ChildWaitOpened{waitID: waitID, spec: c.spec})
	return nil
}

func (c childWaitOperation) validateTree(t *treeSnapshotValidation, parent ProcessID, _ preparedEffect) error {
	if t.processes[parent].status().Terminal() {
		return nil
	}
	return c.spec.validateRelations(parent, t.processRelation)
}

type childStartOperation struct{ spec ChildSpec }

// A started child's identity follows from the Effect; the request fixes its
// key and Deployment.
func (c childStartOperation) settlement(id EffectID, failure Failure) (Settlement, error) {
	result := ChildStartResult{key: c.spec.Key, deploymentRef: c.spec.DeploymentRef, failure: failure}
	if !failure.Valid() {
		result.processID = id.childProcessID()
	}
	payload, err := result.MarshalJSON()
	if err != nil {
		return Settlement{}, err
	}
	return NewSettlement(id, result.settlementStatus(), payload)
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
	signal, err := record.settlementSignal(WaitID{})
	if err != nil {
		return err
	}
	return finalization.enqueueSettlement(signal)
}

func (c childStartOperation) validateTree(t *treeSnapshotValidation, parent ProcessID, record preparedEffect) error {
	if !record.definitelySettled() {
		return nil
	}
	result, err := decodeChildStartResult(record.settlement().Payload())
	if err != nil {
		return err
	}
	childID, started := result.ProcessID()
	if !started {
		return nil
	}
	digest, err := c.spec.digest()
	if err != nil {
		return err
	}
	child, exists := t.processes[childID]
	if !exists {
		return fmt.Errorf("%w: started child is missing", ErrInvalidChildStart)
	}
	if actualParent, _ := child.Relation.ParentID(); actualParent != parent {
		return fmt.Errorf("%w: child parent identity disagrees with start", ErrInvalidChildStart)
	}
	if key, _ := child.Relation.ChildKey(); key != c.spec.Key {
		return fmt.Errorf("%w: child key disagrees with start", ErrInvalidChildStart)
	}
	if child.DeploymentRef != c.spec.DeploymentRef {
		return fmt.Errorf("%w: child Deployment disagrees with start", ErrInvalidChildStart)
	}
	if child.Budget != c.spec.Budget {
		return fmt.Errorf("%w: child budget disagrees with start", ErrInvalidChildStart)
	}
	if !slices.Equal(child.Capabilities.Values(), c.spec.Capabilities.Values()) {
		return fmt.Errorf("%w: child capabilities disagree with start", ErrInvalidChildStart)
	}
	if child.ChildRequestDigest == nil || *child.ChildRequestDigest != digest {
		return fmt.Errorf("%w: child request digest disagrees with start", ErrInvalidChildStart)
	}
	return nil
}

type childControlOperation struct{ request childControlEffectWire }

// The request fixes the recipient, operation, and delivered SignalID.
func (c childControlOperation) settlement(id EffectID, failure Failure) (Settlement, error) {
	result := c.request.result()
	result.failure = failure
	payload, err := result.MarshalJSON()
	if err != nil {
		return Settlement{}, err
	}
	return NewSettlement(id, result.settlementStatus(), payload)
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
	signal, err := record.settlementSignal(WaitID{})
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
	child, present := t.processes[result.childID]
	if actualParent, _ := child.Relation.ParentID(); !present || actualParent != parent {
		return ErrInvalidChildControl
	}
	if result.operation == frameworkOperationCancelChild {
		if !child.status().Terminal() && !child.PendingControl.CancellationOwner.valid() {
			return ErrInvalidChildControl
		}
		return nil
	}
	for _, receipt := range child.Mailbox.receipts() {
		if receipt.ID() != result.signalID {
			continue
		}
		if !receipt.Matches(*c.request.Signal) {
			return ErrInvalidChildControl
		}
		return nil
	}
	return ErrInvalidChildControl
}
