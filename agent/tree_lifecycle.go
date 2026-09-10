package agent

import (
	"cmp"
	"slices"
)

type childWaitRegistration struct {
	parent ProcessID
	waitID WaitID
	spec   ChildWaitSpec
}

func (t *treeRuntime) finishIfTerminal(process *processState) {
	if t.fault != nil || process == nil || !process.status.Terminal() {
		return
	}
	if t.engine.durability != nil {
		t.stageTerminal(process)
		return
	}
	select {
	case <-process.handle.outcomePublished:
		return
	default:
	}
	t.publishEvent(process, EventProcessFinished, EventPhaseCommitted, 0, EffectID{},
		terminalEventPayload(process),
	)
	process.handle.publishResult(process.result())
	t.propagateProcessTermination(process)
	process.handle.finishBookkeeping()
}

func (t *treeRuntime) propagateProcessTermination(process *processState) {
	processID := process.handle.processID
	for waitID, registration := range t.childWaits {
		if registration.parent == processID {
			delete(t.childWaits, waitID)
		}
	}
	t.stopProcessTree(process)
	t.notifyChildWaits(processID, ChildWaitBoundaryResult)
}

func (t *treeRuntime) notifyChildWaits(processID ProcessID, boundary ChildWaitBoundary) {
	if t.fault != nil {
		return
	}
	for _, registration := range orderedChildWaitRegistrations(t.childWaits) {
		if registration.spec.Boundary != boundary || !containsProcessID(registration.spec.Children, processID) {
			continue
		}
		parent := t.processes[registration.parent]
		if parent == nil || parent.status.Terminal() || parent.pendingControl.hasTerminalIntent() ||
			parent.mailbox.contains(deriveChildWaitSignalID(registration.waitID)) {
			continue
		}
		outcomes, satisfied := t.childWaitOutcomes(registration)
		if !satisfied {
			continue
		}
		signal, err := encodeChildWaitSatisfied(registration.waitID, registration.spec.Key, boundary, outcomes)
		if err != nil {
			parent.recordFailure(FailureKindExecution, "engine.child.wait.satisfaction.encoding_failed", err)
			t.stopProcessTree(parent)
			continue
		}
		if t.deliverChildWaitSatisfied(parent, signal) {
			t.enqueueProcess(parent.handle.processID)
		} else if parent.pendingControl.hasTerminalIntent() {
			t.stopProcessTree(parent)
		}
	}
}

func orderedChildWaitRegistrations(
	registrations map[WaitID]*childWaitRegistration,
) []*childWaitRegistration {
	ordered := make([]*childWaitRegistration, 0, len(registrations))
	for _, registration := range registrations {
		ordered = append(ordered, registration)
	}
	slices.SortFunc(ordered, func(left, right *childWaitRegistration) int {
		return cmp.Compare(left.waitID.String(), right.waitID.String())
	})
	return ordered
}

func containsProcessID(processes []ProcessID, processID ProcessID) bool {
	for _, candidate := range processes {
		if candidate == processID {
			return true
		}
	}
	return false
}

func (t *treeRuntime) childWaitOutcomes(
	registration *childWaitRegistration,
) ([]ChildOutcome, bool) {
	outcomes := make([]ChildOutcome, 0, len(registration.spec.Children))
	for _, childID := range registration.spec.Children {
		child := t.processes[childID]
		if child == nil {
			return nil, false
		}
		ready := child.status.Terminal()
		if registration.spec.Boundary == ChildWaitBoundaryDrained {
			ready = child.handle.joinDone() && child.handle.joinError() == nil
		}
		if ready {
			key, _ := child.handle.relation.ChildKey()
			outcomes = append(outcomes, ChildOutcome{key: key, result: child.result()})
		}
	}
	required, err := registration.spec.Condition.required(len(registration.spec.Children))
	return outcomes, err == nil && uint32(len(outcomes)) >= required
}

func (t *treeRuntime) registerChildWait(
	parentID ProcessID,
	waitID WaitID,
	spec ChildWaitSpec,
) (Signal, bool, error) {
	if !parentID.Valid() || !waitID.Valid() || !spec.Valid() || t.processes[parentID] == nil {
		return Signal{}, false, ErrInvalidChildWait
	}
	if t.childWaits[waitID] != nil {
		return Signal{}, false, ErrInvalidChildWait
	}
	for _, childID := range spec.Children {
		child := t.processes[childID]
		if child == nil {
			return Signal{}, false, ErrInvalidChildWait
		}
		actualParent, isChild := child.handle.relation.ParentID()
		if !isChild || actualParent != parentID {
			return Signal{}, false, ErrInvalidChildWait
		}
	}
	registration := &childWaitRegistration{
		parent: parentID,
		waitID: waitID,
		spec:   cloneChildWaitSpec(spec),
	}
	t.childWaits[waitID] = registration
	outcomes, satisfied := t.childWaitOutcomes(registration)
	if !satisfied {
		return Signal{}, false, nil
	}
	signal, err := encodeChildWaitSatisfied(waitID, spec.Key, spec.Boundary, outcomes)
	if err != nil {
		delete(t.childWaits, waitID)
		return Signal{}, false, err
	}
	return signal, true, nil
}

func (t *treeRuntime) unregisterChildWait(waitID WaitID) {
	delete(t.childWaits, waitID)
}
