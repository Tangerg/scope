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
// acquired or held. active is atomic because Engine.Close also reads it
// without the owner; only the tree owner goroutine changes it.
type freezeBarrier struct {
	active atomic.Pointer[activeTreeFreeze]
}

// engaged reports a freeze being acquired or held.
func (f *freezeBarrier) engaged() bool { return f.active.Load() != nil }

// held reports a freeze whose snapshot was granted to its caller.
func (f *freezeBarrier) held() bool {
	active := f.active.Load()
	return active != nil && active.answered()
}

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
	active := f.active.Load()
	if active == nil {
		return nil
	}
	return active.canceled
}

func (f *freezeBarrier) begin(acquisition *treeFreezeAcquisition, freeze *treeFreeze) {
	active := &activeTreeFreeze{acquisition: acquisition, freeze: freeze, canceled: acquisition.canceled}
	if !f.active.CompareAndSwap(nil, active) {
		panic("agent: concurrent tree freeze")
	}
}

func (f *freezeBarrier) owns(freeze *treeFreeze) bool {
	active := f.active.Load()
	return active != nil && freeze != nil && active.freeze == freeze
}

func (f *freezeBarrier) grant(snapshot TreeSnapshot) {
	active := f.active.Load()
	active.answer(treeFreezeAcquisitionResult{freeze: active.freeze, snapshot: snapshot})
}

// end removes the barrier and returns it so a caller still acquiring it can
// be answered.
func (f *freezeBarrier) end() *activeTreeFreeze {
	return f.active.Swap(nil)
}

// treeFreeze identifies the active snapshot barrier. Only CaptureTree receives
// it, and releasing it lets the same tree owner resume scheduling.
type treeFreeze struct {
	runtime *treeRuntime
}

func (t *treeFreeze) release() error {
	response := make(chan error, 1)
	select {
	case t.runtime.freezeCommands <- releaseFreezeCommand{freeze: t, response: response}:
	case <-t.runtime.done:
		return ErrEngineQuiescenceUnavailable
	}
	select {
	case err := <-response:
		return err
	case <-t.runtime.done:
		return ErrEngineQuiescenceUnavailable
	}
}
