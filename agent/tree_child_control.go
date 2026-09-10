package agent

import (
	"errors"
	"time"
)

const (
	childControlNotOwnedCode = "engine.child.control.not_owned"
	childSignalRejectedCode  = "engine.child.signal.rejected"
)

// A tree-local control and its receipt change one authoritative cut. The target
// cannot consume the new input before that cut is acknowledged in durable mode.
func (t *treeRuntime) controlChild(parent *processState, index uint32, record *preparedEffect, startedAt time.Time) {
	request, err := decodeChildControlEffect(record.Effect.Payload())
	if err != nil {
		record.revokeDispatch()
		t.failPreparedEffect(parent, "engine.child.control.invalid", err)
		return
	}
	result := request.result()
	child := t.processes[request.ChildID]
	if child == nil || child.handle.relation.parentID != parent.handle.processID {
		result.failure = newEngineFailure(FailureKindContract, childControlNotOwnedCode,
			errors.New("control recipient is not a direct child"))
	} else {
		result = t.applyChildControl(child, request)
	}
	payload, err := result.MarshalJSON()
	if err == nil {
		status := SettlementStatusSucceeded
		if result.failure.Valid() {
			status = SettlementStatusFailed
		}
		var settlement Settlement
		settlement, err = NewSettlement(record.ID, status, payload)
		if err == nil {
			err = record.settle(settlement)
		}
	}
	if err != nil {
		t.failPreparedEffect(parent, "engine.child.control.settlement.invalid", err)
		return
	}
	t.publishSettlementEvent(parent, record.ID, EffectTargetFramework, record.Settlement.Status(), startedAt, nil)
	t.enqueueProcess(parent.handle.processID)
	if t.engine.durability == nil {
		return
	}
	snapshot, err := t.captureTree()
	if err == nil {
		var boundary EffectBoundary
		boundary, err = newEffectBoundary(EffectBoundarySettled, t.effectRequestFor(parent, index, *record),
			*record.Settlement, t.head.digest(), snapshot)
		if err == nil {
			t.startEffectCommit(&treeCommit{
				kind: treeCommitEffectSettled, processID: parent.handle.processID,
				effectID: record.ID, snapshot: snapshot,
			}, boundary)
		}
	}
	if err != nil {
		t.failDurability(err, parent.handle.processID, record.ID)
	}
}

func (t *treeRuntime) applyChildControl(child *processState, request childControlEffectWire) ChildControlResult {
	result := request.result()
	if request.Operation == frameworkEffectCancelChild {
		if !child.status.Terminal() {
			// The request decoder has already validated this exact reason.
			child.requestCancellation(cancellationIntent{owner: cancellationOwnerParent, reason: request.Reason})
			t.stopProcessTree(child)
		}
		return result
	}
	signal, err := request.Signal.signal()
	if err != nil {
		result.failure = newEngineFailure(FailureKindContract, childSignalRejectedCode, err)
		return result
	}
	accepted, err := child.admitSignals([]Signal{signal}, signalSourceExternal)
	if err != nil {
		result.failure = newEngineFailure(FailureKindExecution, childSignalRejectedCode, err)
		return result
	}
	if accepted {
		t.publishEphemeralStatus(child)
		for _, event := range t.prepareSignalEvents(child, []Signal{signal}) {
			t.publishPreparedEventAfterCommit(child, event)
		}
		t.enqueueProcess(child.handle.processID)
	}
	return result
}
