package agent

import "slices"

// runQueue is the FIFO of Processes awaiting a scheduling turn. Membership
// mirrors order exactly, so a Process is never queued twice or left behind
// after removal.
type runQueue struct {
	order   []ProcessID
	members map[ProcessID]struct{}
}

func newRunQueue(capacity int) runQueue {
	return runQueue{members: make(map[ProcessID]struct{}, capacity)}
}

func (r *runQueue) push(processID ProcessID) {
	if r.contains(processID) {
		return
	}
	r.members[processID] = struct{}{}
	r.order = append(r.order, processID)
}

func (r *runQueue) pop() (ProcessID, bool) {
	if len(r.order) == 0 {
		return ProcessID{}, false
	}
	processID := r.order[0]
	r.order = r.order[1:]
	delete(r.members, processID)
	return processID, true
}

func (r *runQueue) remove(processID ProcessID) {
	if !r.contains(processID) {
		return
	}
	delete(r.members, processID)
	r.order = slices.DeleteFunc(r.order, func(queued ProcessID) bool { return queued == processID })
}

func (r *runQueue) clear() {
	clear(r.members)
	r.order = nil
}

func (r *runQueue) contains(processID ProcessID) bool {
	_, queued := r.members[processID]
	return queued
}

func (r *runQueue) empty() bool { return len(r.order) == 0 }
