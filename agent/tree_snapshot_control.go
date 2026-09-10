package agent

import "fmt"

func (t *treeSnapshotValidation) validateChildControls() error {
	for _, parent := range t.processes {
		if parent.Prepared == nil {
			continue
		}
		for _, record := range parent.Prepared.Effects {
			if record.Effect.Target() != EffectTargetFramework || !record.definitelySettled() {
				continue
			}
			operation, err := decodeFrameworkEffectOperation(record.Effect.Payload())
			if err != nil {
				return err
			}
			if operation != frameworkEffectSignalChild && operation != frameworkEffectCancelChild {
				continue
			}
			if err := t.validateChildControl(parent.ProcessID, record); err != nil {
				return fmt.Errorf("%w: child control: %w", ErrInvalidTreeSnapshot, err)
			}
		}
	}
	return nil
}

func (t *treeSnapshotValidation) validateChildControl(parentID ProcessID, record preparedEffect) error {
	if err := record.validateChildControl(); err != nil {
		return err
	}
	result, err := decodeChildControlResult(record.Settlement.Payload())
	if err != nil || result.failure.Valid() {
		return err
	}
	child, present := t.processes[result.childID]
	if !present || child.Relation.ParentID == nil || *child.Relation.ParentID != parentID {
		return ErrInvalidChildControl
	}
	if result.operation == frameworkEffectCancelChild {
		if !child.Status.Terminal() && !child.PendingControl.CancellationOwner.valid() {
			return ErrInvalidChildControl
		}
		return nil
	}
	request, err := decodeChildControlEffect(record.Effect.Payload())
	if err != nil {
		return err
	}
	signal, err := request.Signal.signal()
	if err != nil {
		return err
	}
	for _, receipt := range child.Mailbox.Signals {
		if receipt.ID != result.signalID {
			continue
		}
		var waitID WaitID
		if receipt.WaitID != nil {
			waitID = *receipt.WaitID
		}
		if receipt.OpensWait || receipt.PayloadDigest != ComputeDigest(signal.Payload()) || waitID != signal.waitID {
			return ErrInvalidChildControl
		}
		return nil
	}
	return ErrInvalidChildControl
}
