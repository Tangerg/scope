package agent

import "sync/atomic"

type treeFreezeAcquisition struct {
	response chan treeFreezeAcquisitionResult
	canceled chan struct{}
}

type treeFreezeAcquisitionResult struct {
	freeze   *treeFreeze
	snapshot TreeSnapshot
	err      error
}

// Cancellation remains observable after answering: the caller may select its
// canceled context while the acquisition result is still buffered.
type activeTreeFreeze struct {
	acquisition *treeFreezeAcquisition
	freeze      *treeFreeze
	canceled    <-chan struct{}
}

func (a *activeTreeFreeze) answered() bool { return a.acquisition == nil }

func (a *activeTreeFreeze) answer(result treeFreezeAcquisitionResult) {
	if a.acquisition == nil {
		return
	}
	acquisition := a.acquisition
	a.acquisition = nil
	acquisition.response <- result
}

// freezeBarrier owns the tree's snapshot barrier: at most one freeze is being
// acquired or held. engagedFlag mirrors that state for lock-free Engine.Close
// checks; only the tree owner goroutine changes either.
type freezeBarrier struct {
	active      *activeTreeFreeze
	engagedFlag atomic.Bool
}

// engaged reports a freeze being acquired or held.
func (f *freezeBarrier) engaged() bool { return f.active != nil }

// held reports a freeze whose snapshot was granted to its caller.
func (f *freezeBarrier) held() bool { return f.active != nil && f.active.answered() }

func (f *freezeBarrier) phase() TreeFreezePhase {
	switch {
	case f.held():
		return TreeFreezePhaseHeld
	case f.engaged():
		return TreeFreezePhaseAcquiring
	default:
		return TreeFreezePhaseNone
	}
}

func (f *freezeBarrier) cancellation() <-chan struct{} {
	if f.active == nil {
		return nil
	}
	return f.active.canceled
}

func (f *freezeBarrier) begin(acquisition *treeFreezeAcquisition, freeze *treeFreeze) {
	if f.active != nil {
		panic("agent: concurrent tree freeze")
	}
	f.active = &activeTreeFreeze{acquisition: acquisition, freeze: freeze, canceled: acquisition.canceled}
	f.engagedFlag.Store(true)
}

func (f *freezeBarrier) owns(freeze *treeFreeze) bool {
	return f.active != nil && freeze != nil && f.active.freeze == freeze
}

func (f *freezeBarrier) grant(snapshot TreeSnapshot) {
	f.active.answer(treeFreezeAcquisitionResult{freeze: f.active.freeze, snapshot: snapshot})
}

// end removes the barrier and returns it so a caller still acquiring it can
// be answered.
func (f *freezeBarrier) end() *activeTreeFreeze {
	ended := f.active
	f.active = nil
	f.engagedFlag.Store(false)
	return ended
}
