package agent

import (
	"sync"
	"sync/atomic"
	"time"
)

// processHandleState is shared by the Process handles for one execution. The
// tree runtime publishes status and outcomes here without transferring control.
type processHandleState struct {
	// Identity and allocation are immutable after Engine publishes the Process,
	// so callers can inspect them without contending with the runtime owner goroutine.
	processID          ProcessID
	deploymentRef      DeploymentRef
	relation           ProcessRelation
	childRequestDigest Digest
	budget             Budget
	capabilities       CapabilitySet
	treeLimits         TreeLimits
	startedAt          time.Time
	runtime            atomic.Pointer[treeRuntime]

	// Await joins outcome publication and immediate parent/child bookkeeping.
	// The tree runtime separately joins descendant jobs before it stops.
	outcomePublished chan struct{}
	bookkeepingDone  chan struct{}
	bookkeepingOnce  sync.Once

	// mu protects the acknowledged status and retained instance outcome.
	// It is never held while running execution code or invoking listeners.
	mu                 sync.RWMutex
	acknowledgedStatus Status
	result             Result
	runtimeErr         *RuntimeError
}

func newProcessHandleState(
	relation ProcessRelation,
	deploymentRef DeploymentRef,
	budget Budget,
	capabilities CapabilitySet,
	treeLimits TreeLimits,
	startedAt time.Time,
	status Status,
) *processHandleState {
	return &processHandleState{
		processID: relation.ProcessID(), deploymentRef: deploymentRef, relation: relation,
		budget: budget, capabilities: capabilities, treeLimits: treeLimits, startedAt: startedAt,
		outcomePublished: make(chan struct{}),
		bookkeepingDone:  make(chan struct{}), acknowledgedStatus: status,
	}
}

func (p *processHandleState) status() Status {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.acknowledgedStatus
}

func (p *processHandleState) updateStatus(status Status) {
	p.mu.Lock()
	p.acknowledgedStatus = status
	p.mu.Unlock()
}

func (p *processHandleState) publishResult(result Result) {
	p.mu.Lock()
	p.acknowledgedStatus = result.Status()
	p.result = result
	p.mu.Unlock()
	close(p.outcomePublished)
}

func (p *processHandleState) finishBookkeeping() {
	p.bookkeepingOnce.Do(func() { close(p.bookkeepingDone) })
}

func (p *processHandleState) outcome() (Result, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.runtimeErr != nil {
		return Result{}, p.runtimeErr.clone()
	}
	return p.result, nil
}

func (p *processHandleState) publishRuntimeFailure(err *RuntimeError, snapshot ProcessSnapshot) {
	p.mu.Lock()
	p.acknowledgedStatus = snapshot.status
	p.runtimeErr = err
	p.mu.Unlock()
	close(p.outcomePublished)
}

func (p *processHandleState) closedRequestError() error {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.runtimeErr != nil {
		return p.runtimeErr.clone()
	}
	return ErrProcessFinished
}
