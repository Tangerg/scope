package agent

import "slices"

func (t *treeRuntime) addProcess(process *processState) {
	if t == nil || process == nil || process.handle == nil ||
		process.handle.relation.RootID() != t.rootID {
		panic("agent: invalid tree Process")
	}
	processID := process.handle.processID
	if t.processes[processID] != nil {
		panic("agent: duplicate tree Process")
	}
	process.handle.runtime.Store(t)
	t.processes[processID] = process
	t.queueJoin(process)
	if parentID, child := process.handle.relation.ParentID(); child {
		t.childrenByParent[parentID] = append(t.childrenByParent[parentID], processID)
	}
	if !process.status.Terminal() {
		t.enqueueProcess(processID)
	}
}

// Only unpublished children can be removed while the owner is running. Every
// membership change updates the derived parent index at this boundary.
func (t *treeRuntime) removeProcess(processID ProcessID) {
	process := t.processes[processID]
	if process == nil {
		return
	}
	if parentID, child := process.handle.relation.ParentID(); child {
		children := t.childrenByParent[parentID]
		index := slices.Index(children, processID)
		if index < 0 {
			panic("agent: tree child membership is missing")
		}
		children = slices.Delete(children, index, index+1)
		if len(children) == 0 {
			delete(t.childrenByParent, parentID)
		} else {
			t.childrenByParent[parentID] = children
		}
	}
	delete(t.processes, processID)
	delete(t.joinCandidates, processID)
}
