package agent

import (
	"iter"
	"maps"
	"slices"
)

// treeMembers owns the Processes of one tree and the parent index derived from
// their relations. Membership changes only through add and remove, so the
// index always mirrors the members' relations.
type treeMembers struct {
	byID     map[ProcessID]*processState
	children map[ProcessID][]ProcessID
}

func newTreeMembers(capacity int) treeMembers {
	return treeMembers{
		byID:     make(map[ProcessID]*processState, capacity),
		children: make(map[ProcessID][]ProcessID),
	}
}

func (t *treeMembers) get(processID ProcessID) *processState { return t.byID[processID] }

func (t *treeMembers) len() int { return len(t.byID) }

func (t *treeMembers) all() iter.Seq2[ProcessID, *processState] { return maps.All(t.byID) }

// ordered returns members in canonical tree order.
func (t *treeMembers) ordered() []*processState { return orderedProcesses(t.byID) }

// childrenOf returns processID's direct children in admission order.
func (t *treeMembers) childrenOf(processID ProcessID) []ProcessID { return t.children[processID] }

func (t *treeMembers) relation(processID ProcessID) ProcessRelation {
	if process := t.byID[processID]; process != nil {
		return process.handle.relation
	}
	return ProcessRelation{}
}

func (t *treeMembers) subtreeUnresolvedEffects(processID ProcessID) []UnresolvedEffect {
	return subtreeUnresolvedEffects(processID, t.childrenOf,
		func(id ProcessID) Termination { return t.byID[id].termination })
}

func (t *treeMembers) add(process *processState) {
	processID := process.handle.processID
	if t.byID[processID] != nil {
		panic("agent: duplicate tree Process")
	}
	t.byID[processID] = process
	if parentID, child := process.handle.relation.ParentID(); child {
		t.children[parentID] = append(t.children[parentID], processID)
	}
}

func (t *treeMembers) remove(processID ProcessID) {
	process := t.byID[processID]
	if process == nil {
		return
	}
	if parentID, child := process.handle.relation.ParentID(); child {
		children := t.children[parentID]
		index := slices.Index(children, processID)
		if index < 0 {
			panic("agent: tree child membership is missing")
		}
		if children = slices.Delete(children, index, index+1); len(children) == 0 {
			delete(t.children, parentID)
		} else {
			t.children[parentID] = children
		}
	}
	delete(t.byID, processID)
}

// substituted yields the tree a candidate cut would contain: every member,
// replaced by its candidate when one exists, followed by candidates for
// Processes that are not members yet.
func (t *treeMembers) substituted(candidates []*processState) iter.Seq[*processState] {
	return func(yield func(*processState) bool) {
		replacements := make(map[ProcessID]*processState, len(candidates))
		for _, candidate := range candidates {
			replacements[candidate.handle.processID] = candidate
		}
		for processID, member := range t.byID {
			if replacement := replacements[processID]; replacement != nil {
				member = replacement
			}
			if !yield(member) {
				return
			}
		}
		for _, candidate := range candidates {
			if t.byID[candidate.handle.processID] == nil && !yield(candidate) {
				return
			}
		}
	}
}

func (t *treeMembers) allTerminal() bool {
	for _, process := range t.byID {
		if !process.status.Terminal() {
			return false
		}
	}
	return true
}
