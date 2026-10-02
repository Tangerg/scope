package agent

import (
	"bytes"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"slices"

	"github.com/Tangerg/scope/agent/internal/jsonwire"
)

// The closed protocol binds decoding, preparation, execution and recovery for
// each framework operation. Consumers cannot choose different interpretations
// of the same payload at different lifecycle boundaries.
type frameworkOperation interface {
	validate(*preparedEffect) error
	settle(*preparedEffect) error
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

func (w waitOperation) validate(effect *preparedEffect) error {
	if err := effect.validateWait(); err != nil {
		return err
	}
	if effect.Settlement != nil && !bytes.Equal(effect.Settlement.payload, w.payload) {
		return errors.New("wait Effect settlement differs from its request")
	}
	return nil
}

func (w waitOperation) settle(effect *preparedEffect) error { return effect.settleWait(w.payload) }
func (w waitOperation) reserve(effect *preparedEffect, _ Failure) (uint64, error) {
	if effect.Phase == effectPhasePlanned {
		if err := effect.begin(); err != nil {
			return 0, err
		}
	}
	return 0, w.settle(effect)
}

func (w waitOperation) apply(finalization *preparedStepFinalization, record preparedEffect) error {
	signal, err := record.settlementSignal(record.ID.waitID())
	if err != nil {
		return err
	}
	return finalization.mailbox.openWait(w.key, signal, WaitKindExternal)
}

func (w waitOperation) validateTree(*treeSnapshotValidation, ProcessID, preparedEffect) error {
	return nil
}

type childWaitOperation struct{ spec ChildWaitSpec }

func (c childWaitOperation) validate(effect *preparedEffect) error {
	if err := effect.validateWait(); err != nil {
		return err
	}
	if effect.Settlement == nil {
		return nil
	}
	opened, err := jsonwire.Decode[childWaitOpenedWire](effect.Settlement.payload)
	if err != nil {
		return err
	}
	got, err := opened.Spec.value()
	if err != nil {
		return err
	}
	if opened.Operation != childWaitSignalOpened || !got.equal(c.spec) {
		return errors.New("child-wait Effect settlement differs from its request")
	}
	return nil
}

func (c childWaitOperation) settle(effect *preparedEffect) error {
	payload, err := encodeChildWaitOpened(c.spec)
	if err != nil {
		return err
	}
	return effect.settleWait(payload)
}

func (c childWaitOperation) reserve(effect *preparedEffect, _ Failure) (uint64, error) {
	if effect.Phase == effectPhasePlanned {
		if err := effect.begin(); err != nil {
			return 0, err
		}
	}
	return 0, c.settle(effect)
}

func (c childWaitOperation) apply(finalization *preparedStepFinalization, record preparedEffect) error {
	waitID := record.ID.waitID()
	signal, err := record.settlementSignal(waitID)
	if err != nil {
		return err
	}
	if err := finalization.mailbox.openWait(c.spec.Key, signal, WaitKindChildren); err != nil {
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

func (c childStartOperation) validate(effect *preparedEffect) error {
	if effect.Settlement == nil {
		return nil
	}
	spec := c.spec
	result, err := decodeChildStartResult(effect.Settlement.Payload())
	if err != nil {
		return err
	}
	if result.Key() != spec.Key || result.DeploymentRef() != spec.DeploymentRef {
		return ErrInvalidChildStart
	}
	if id, started := result.ProcessID(); started && id != effect.ID.childProcessID() {
		return ErrInvalidChildStart
	}
	if effect.Settlement.Status() != result.settlementStatus() {
		return ErrInvalidChildStart
	}
	return nil
}

func (c childStartOperation) settle(*preparedEffect) error {
	return fmt.Errorf("%w: child start requires its job outcome", ErrInvalidEffect)
}

func (c childStartOperation) reserve(effect *preparedEffect, failure Failure) (uint64, error) {
	if effect.Phase != effectPhasePending {
		return 0, nil
	}
	return snapshotFailureGrowth, effect.settleChildStart(ChildStartResult{key: c.spec.Key, deploymentRef: c.spec.DeploymentRef, failure: failure})
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
	result, err := decodeChildStartResult(record.Settlement.Payload())
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
	if child.Relation.ParentID == nil || *child.Relation.ParentID != parent {
		return fmt.Errorf("%w: child parent identity disagrees with start", ErrInvalidChildStart)
	}
	if child.Relation.ChildKey == nil || *child.Relation.ChildKey != c.spec.Key {
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

func (c childControlOperation) validate(effect *preparedEffect) error {
	if effect.Settlement == nil {
		return nil
	}
	result, err := decodeChildControlResult(effect.Settlement.Payload())
	if err != nil || !result.matches(c.request) {
		return ErrInvalidChildControl
	}
	if effect.Settlement.Status() != result.settlementStatus() {
		return ErrInvalidChildControl
	}
	return nil
}

func (c childControlOperation) settle(*preparedEffect) error {
	return fmt.Errorf("%w: child control requires its recipient outcome", ErrInvalidEffect)
}

func (c childControlOperation) reserve(effect *preparedEffect, failure Failure) (uint64, error) {
	if effect.Phase != effectPhasePending {
		return 0, nil
	}
	result := c.request.result()
	result.failure = failure
	return snapshotFailureGrowth, effect.settleChildControl(result)
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
	result, err := decodeChildControlResult(record.Settlement.Payload())
	if err != nil || result.failure.Valid() {
		return err
	}
	child, present := t.processes[result.childID]
	if !present || child.Relation.ParentID == nil || *child.Relation.ParentID != parent {
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
