package agent

import (
	"bytes"
	"encoding/json"
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
	apply(*preparedStepFinalization, preparedEffect, Signal) error
	validateTree(*treeSnapshotValidation, ProcessID, preparedEffect) error
	dispatch(*treeRuntime, *processState, uint32, *preparedEffect, effectAttempt)
}

func decodeFrameworkOperation(payload json.RawMessage) (frameworkOperation, error) {
	operation, err := decodeFrameworkEffectOperation(payload)
	if err != nil {
		return nil, err
	}
	switch operation {
	case frameworkEffectWait:
		key, signal, err := decodeWaitRequestPayload(payload)
		return waitOperation{key: key, payload: signal}, err
	case frameworkEffectWaitChildren:
		spec, err := decodeChildWaitEffect(payload)
		return childWaitOperation{spec: spec}, err
	case frameworkEffectStartChild:
		spec, err := decodeChildStartEffect(payload)
		return childStartOperation{spec: spec}, err
	case frameworkEffectSignalChild, frameworkEffectCancelChild:
		request, err := decodeChildControlEffect(payload)
		return childControlOperation{request: request}, err
	default:
		return nil, ErrInvalidEffect
	}
}

type waitOperation struct {
	key     WaitKey
	payload json.RawMessage
}

func (w waitOperation) validate(p *preparedEffect) error {
	if err := p.validateWait(); err != nil {
		return err
	}
	if p.Settlement != nil && !bytes.Equal(p.Settlement.payload, w.payload) {
		return errors.New("wait Effect settlement differs from its request")
	}
	return nil
}
func (w waitOperation) settle(p *preparedEffect) error { return p.settleWait(w.payload) }
func (w waitOperation) reserve(p *preparedEffect, _ Failure) (uint64, error) {
	if p.Phase == effectPhasePlanned {
		if err := p.begin(); err != nil {
			return 0, err
		}
	}
	return 0, w.settle(p)
}
func (w waitOperation) apply(p *preparedStepFinalization, _ preparedEffect, signal Signal) error {
	return p.mailbox.openWait(w.key, signal, WaitKindExternal)
}
func (w waitOperation) validateTree(*treeSnapshotValidation, ProcessID, preparedEffect) error {
	return nil
}
func (w waitOperation) dispatch(t *treeRuntime, p *processState, _ uint32, record *preparedEffect, observation effectAttempt) {
	t.settleFramework(p, record, observation)
}

type childWaitOperation struct{ spec ChildWaitSpec }

func (c childWaitOperation) validate(p *preparedEffect) error {
	if err := p.validateWait(); err != nil {
		return err
	}
	if p.Settlement == nil {
		return nil
	}
	opened, err := jsonwire.Decode[childWaitOpenedWire](p.Settlement.payload)
	if err != nil {
		return err
	}
	got, err := opened.Spec.value()
	if err != nil {
		return err
	}
	if opened.Operation != childSignalWaitOpened || got.Key != c.spec.Key || got.Boundary != c.spec.Boundary || got.Condition != c.spec.Condition || !slices.Equal(got.Children, c.spec.Children) {
		return errors.New("child-wait Effect settlement differs from its request")
	}
	return nil
}
func (c childWaitOperation) settle(p *preparedEffect) error {
	payload, err := encodeChildWaitOpened(c.spec)
	if err != nil {
		return err
	}
	return p.settleWait(payload)
}
func (c childWaitOperation) reserve(p *preparedEffect, _ Failure) (uint64, error) {
	if p.Phase == effectPhasePlanned {
		if err := p.begin(); err != nil {
			return 0, err
		}
	}
	return 0, c.settle(p)
}
func (c childWaitOperation) apply(p *preparedStepFinalization, record preparedEffect, signal Signal) error {
	if record.WaitID == nil {
		return ErrInvalidChildWait
	}
	if err := p.mailbox.openWait(c.spec.Key, signal, WaitKindChildren); err != nil {
		return err
	}
	p.openedChildWaits = append(p.openedChildWaits, ChildWaitOpened{waitID: *record.WaitID, spec: c.spec})
	return nil
}
func (c childWaitOperation) validateTree(*treeSnapshotValidation, ProcessID, preparedEffect) error {
	return nil
}
func (c childWaitOperation) dispatch(t *treeRuntime, p *processState, _ uint32, record *preparedEffect, observation effectAttempt) {
	t.settleFramework(p, record, observation)
}

type childStartOperation struct{ spec ChildSpec }

func (c childStartOperation) settle(*preparedEffect) error {
	return fmt.Errorf("%w: child start requires its job outcome", ErrInvalidEffect)
}
func (c childStartOperation) reserve(p *preparedEffect, failure Failure) (uint64, error) {
	if p.Phase != effectPhasePending {
		return 0, nil
	}
	return snapshotFailureGrowth, p.settleChildStart(ChildStartResult{key: c.spec.Key, deploymentRef: c.spec.DeploymentRef, failure: failure})
}
func (c childStartOperation) apply(p *preparedStepFinalization, record preparedEffect, signal Signal) error {
	if record.WaitID != nil {
		return ErrInvalidChildStart
	}
	return p.enqueueSettlement(signal)
}
func (c childStartOperation) validateTree(t *treeSnapshotValidation, parent ProcessID, record preparedEffect) error {
	return t.validateChildStart(parent, record)
}
func (c childStartOperation) dispatch(t *treeRuntime, p *processState, _ uint32, record *preparedEffect, observation effectAttempt) {
	t.startChild(p, record, observation)
}

type childControlOperation struct{ request childControlEffectWire }

func (c childControlOperation) settle(*preparedEffect) error {
	return fmt.Errorf("%w: child control requires its recipient outcome", ErrInvalidEffect)
}
func (c childControlOperation) reserve(p *preparedEffect, failure Failure) (uint64, error) {
	if p.Phase != effectPhasePending {
		return 0, nil
	}
	result := c.request.result()
	result.failure = failure
	return snapshotFailureGrowth, p.settleChildControl(result)
}
func (c childControlOperation) apply(p *preparedStepFinalization, record preparedEffect, signal Signal) error {
	if record.WaitID != nil {
		return ErrInvalidChildControl
	}
	return p.enqueueSettlement(signal)
}
func (c childControlOperation) validateTree(t *treeSnapshotValidation, parent ProcessID, record preparedEffect) error {
	return t.validateChildControl(parent, record)
}
func (c childControlOperation) dispatch(t *treeRuntime, p *processState, index uint32, record *preparedEffect, observation effectAttempt) {
	t.controlChild(p, index, record, observation)
}
func (c childStartOperation) validate(p *preparedEffect) error {
	if p.WaitID != nil {
		return ErrInvalidChildStart
	}
	if p.Settlement == nil {
		return nil
	}
	spec := c.spec
	result, err := decodeChildStartResult(p.Settlement.Payload())
	if err != nil {
		return err
	}
	if result.Key() != spec.Key || result.DeploymentRef() != spec.DeploymentRef {
		return ErrInvalidChildStart
	}
	status := SettlementStatusFailed
	if id, started := result.ProcessID(); started {
		if id != p.ID.childProcessID() {
			return ErrInvalidChildStart
		}
		status = SettlementStatusSucceeded
	}
	if p.Settlement.Status() != status {
		return ErrInvalidChildStart
	}
	return nil
}

func (c childControlOperation) validate(p *preparedEffect) error {
	if p.WaitID != nil {
		return ErrInvalidChildControl
	}
	if p.Settlement == nil {
		return nil
	}
	request := c.request
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
