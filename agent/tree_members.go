package agent

import (
	"iter"
	"maps"
	"slices"
)

// treeMembers owns the Processes of one tree and the indexes derived from
// their relations and immutable grants. Membership changes only through add and
// remove, so the parent index and each parent's child debits always mirror the
// members.
type treeMembers struct {
	byID             map[ProcessID]*processState
	children         map[ProcessID][]ProcessID
	childAllocations map[ProcessID]resourceAmounts
}

func newTreeMembers(capacity int) treeMembers {
	return treeMembers{
		byID:             make(map[ProcessID]*processState, capacity),
		children:         make(map[ProcessID][]ProcessID),
		childAllocations: make(map[ProcessID]resourceAmounts),
	}
}

func (t *treeMembers) get(processID ProcessID) *processState { return t.byID[processID] }

func (t *treeMembers) len() int { return len(t.byID) }

func (t *treeMembers) all() iter.Seq2[ProcessID, *processState] { return maps.All(t.byID) }

// ordered returns members in canonical tree order.
func (t *treeMembers) ordered() []*processState { return orderedProcesses(t.byID) }

// childAllocation returns the finite debits processID's member children hold
// against its budget.
func (t *treeMembers) childAllocation(processID ProcessID) resourceAmounts {
	return t.childAllocations[processID]
}

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
	if parentID, child := process.handle.relation.ParentID(); child {
		parent := t.byID[parentID]
		if parent == nil {
			panic("agent: tree child requires its parent")
		}
		debit, ok := parent.handle.budget.allocation(process.handle.budget)
		allocated, fits := t.childAllocations[parentID].add(debit)
		if !ok || !fits {
			panic("agent: child grant exceeds parent authority")
		}
		t.childAllocations[parentID] = allocated
		t.children[parentID] = append(t.children[parentID], processID)
	}
	t.byID[processID] = process
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
			delete(t.childAllocations, parentID)
		} else {
			t.children[parentID] = children
			if parent := t.byID[parentID]; parent != nil {
				debit, _ := parent.handle.budget.allocation(process.handle.budget)
				t.childAllocations[parentID] = t.childAllocations[parentID].subtract(debit)
			}
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
		if !process.status().Terminal() {
			return false
		}
	}
	return true
}

// childWaitAnswer returns the answer Signal once enough watched children have
// reached the wait's boundary. Opening the wait proved membership, and
// children stay retained until tree release.
func (t *treeMembers) childWaitAnswer(opened ChildWaitOpened) (Signal, bool, error) {
	spec := opened.spec
	outcomes := make([]ChildOutcome, 0, len(spec.Children))
	for _, childID := range spec.Children {
		child := t.get(childID)
		ready := child.status().Terminal()
		if spec.Boundary == ChildWaitBoundaryDrained {
			ready = child.handle.joinDone() && child.handle.joinError() == nil
		}
		if !ready {
			continue
		}
		key, _ := child.handle.relation.ChildKey()
		outcome := ChildOutcome{key: key, result: child.result(), boundary: spec.Boundary}
		if spec.Boundary == ChildWaitBoundaryDrained {
			outcome.subtreeUnresolvedEffects = t.subtreeUnresolvedEffects(childID)
		}
		outcomes = append(outcomes, outcome)
	}
	if uint32(len(outcomes)) < spec.required() {
		return Signal{}, false, nil
	}
	signal, err := encodeChildWaitSatisfied(opened.waitID, spec.Key, spec.Boundary, outcomes)
	if err != nil {
		return Signal{}, false, err
	}
	return signal, true, nil
}
