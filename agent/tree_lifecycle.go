package agent

import (
	"cmp"
	"slices"
)

func (t *treeRuntime) finishIfTerminal(process *processState) {
	if process == nil || !process.status.Terminal() {
		return
	}
	if t.engine.durability != nil && !t.durabilityFault {
		t.stageTerminal(process)
		return
	}
	select {
	case <-process.controller.done:
		return
	default:
	}
	process.publishEvent(
		t.context, EventProcessFinished, EventPhaseCommitted, 0, EffectID{},
		terminalEventPayload(process),
	)
	snapshot, err := process.capture()
	process.controller.complete(process.result(), snapshot, err)
	t.processFinished(process)
	process.controller.markTreeSettled()
}

func (t *treeRuntime) processFinished(process *processState) {
	processID := process.controller.processID
	for waitID, registration := range t.childWaits {
		if registration.parent == processID {
			delete(t.childWaits, waitID)
		}
	}
	for _, child := range t.processes {
		parentID, isChild := child.controller.relation.ParentID()
		if isChild && parentID == processID && !child.status.Terminal() {
			child.recordParentTermination(process.termination)
			t.invalidateStep(child)
			t.markRunnable(child.controller.processID)
		}
	}
	for _, registration := range orderedChildWaitRegistrations(t.childWaits) {
		if registration.delivered || !containsProcessID(registration.spec.Children, processID) {
			continue
		}
		outcomes, satisfied := t.childWaitOutcomes(registration)
		if !satisfied {
			continue
		}
		parent := t.processes[registration.parent]
		if parent == nil || parent.status.Terminal() || parent.pendingControl.hasTerminalIntent() {
			continue
		}
		signal, err := encodeChildrenCompleted(registration.waitID, registration.spec.Key, outcomes)
		if err != nil {
			parent.recordFailure(FailureKindExecution, "engine.child.completion.encoding_failed", err)
			t.invalidateStep(parent)
			t.markRunnable(parent.controller.processID)
			continue
		}
		if parent.deliverChildrenCompleted(t.context, signal) {
			registration.delivered = true
			t.markRunnable(parent.controller.processID)
		} else if parent.pendingControl.hasTerminalIntent() {
			t.invalidateStep(parent)
			t.markRunnable(parent.controller.processID)
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
		if child.status.Terminal() {
			key, _ := child.controller.relation.ChildKey()
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
		actualParent, isChild := child.controller.relation.ParentID()
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
	signal, err := encodeChildrenCompleted(waitID, spec.Key, outcomes)
	if err != nil {
		delete(t.childWaits, waitID)
		return Signal{}, false, err
	}
	registration.delivered = true
	return signal, true, nil
}

func (t *treeRuntime) unregisterChildWait(waitID WaitID) {
	delete(t.childWaits, waitID)
}
