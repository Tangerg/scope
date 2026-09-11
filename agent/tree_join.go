package agent

import "errors"

// A result can precede descendant cleanup. Publish each join once, from leaves
// upward, only after acknowledged outcomes and the owned calls have returned.
func (t *treeRuntime) publishJoins() bool {
	if t.commit != nil || t.freeze != nil {
		return false
	}
	changed := false
	for len(t.joinCandidates) != 0 {
		processes := orderedProcesses(t.joinCandidates)
		for index := len(processes) - 1; index >= 0; index-- {
			process := processes[index]
			delete(t.joinCandidates, process.handle.processID)
			changed = t.publishJoin(process) || changed
		}
	}
	return changed
}

func (t *treeRuntime) publishJoin(process *processState) bool {
	if process.handle.joinDone() {
		return false
	}
	if t.jobs[process.handle.processID] != nil {
		return false
	}
	select {
	case <-process.handle.bookkeepingDone:
	default:
		return false
	}
	_, outcomeErr := process.handle.outcome()
	failure, failed := errors.AsType[*RuntimeError](outcomeErr)
	var unresolved []EffectID
	if failed {
		unresolved = append(unresolved, failure.UnresolvedEffectIDs()...)
	}
	ready := true
	for _, childID := range t.childrenByParent[process.handle.processID] {
		child := t.processes[childID]
		select {
		case <-child.handle.joined:
			if childFailure, ok := errors.AsType[*RuntimeError](child.handle.joinError()); ok {
				failed = true
				unresolved = append(unresolved, childFailure.UnresolvedEffectIDs()...)
			}
		default:
			ready = false
		}
	}
	if !ready {
		return false
	}
	var joinErr *RuntimeError
	if failed {
		joinErr = &RuntimeError{
			processID: process.handle.processID, incarnationID: t.incarnation,
			headDigest: t.head.digest(), unresolvedEffectIDs: canonicalEffectIDs(unresolved), cause: t.fault,
		}
	}
	process.handle.finishJoin(joinErr)
	if joinErr == nil {
		t.notifyChildWaits(process.handle.processID, ChildWaitBoundaryDrained)
	}
	if parentID, child := process.handle.relation.ParentID(); child {
		t.queueJoin(t.processes[parentID])
	}
	return true
}

// Only changes to result publication, owned calls, or child joins can make a
// Process joinable. A failed check sleeps until one of those facts changes.
func (t *treeRuntime) queueJoin(process *processState) {
	if process == nil || process.handle.joinDone() {
		return
	}
	select {
	case <-process.handle.bookkeepingDone:
		t.joinCandidates[process.handle.processID] = process
	default:
	}
}

func (t *treeRuntime) completeProcessBookkeeping(process *processState) {
	process.handle.finishBookkeeping()
	t.queueJoin(process)
}
