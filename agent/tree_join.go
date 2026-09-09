package agent

import "errors"

// A result can precede descendant cleanup. Publish each join once, from leaves
// upward, only after acknowledged outcomes and the owned calls have returned.
func (t *treeRuntime) publishJoins() bool {
	if t.commit != nil || t.freeze != nil {
		return false
	}
	changed := false
	processes := t.processesInCanonicalOrder()
	for index := len(processes) - 1; index >= 0; index-- {
		process := processes[index]
		if process.handle.joinDone() {
			continue
		}
		if t.jobs[process.handle.processID] != nil {
			continue
		}
		select {
		case <-process.handle.bookkeepingDone:
		default:
			continue
		}
		_, outcomeErr := process.handle.outcome()
		failure, failed := errors.AsType[*RuntimeError](outcomeErr)
		var unresolved []EffectID
		if failed {
			unresolved = append(unresolved, failure.UnresolvedEffectIDs()...)
		}
		ready := true
		for _, child := range processes {
			parentID, hasParent := child.handle.relation.ParentID()
			if !hasParent || parentID != process.handle.processID {
				continue
			}
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
			continue
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
		changed = true
	}
	return changed
}
