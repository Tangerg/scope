package agent

import (
	"sync"
	"sync/atomic"
	"time"
)

// processHandle is shared by the Process handles for one execution. The
// tree runtime publishes outcomes here without transferring control.
type processHandle struct {
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
	// Join additionally waits for owned descendant work and acknowledgments.
	outcomePublished chan struct{}
	bookkeepingDone  chan struct{}
	joined           chan struct{}

	// mu protects the retained instance outcome.
	// It is never held while running execution code or invoking listeners.
	mu sync.RWMutex

	result     Result
	runtimeErr *RuntimeError
	joinErr    *RuntimeError
}

func newProcessHandle(
	relation ProcessRelation,
	deploymentRef DeploymentRef,
	budget Budget,
	capabilities CapabilitySet,
	treeLimits TreeLimits,
	startedAt time.Time,
) *processHandle {
	return &processHandle{
		processID: relation.ProcessID(), deploymentRef: deploymentRef, relation: relation,
		budget: budget, capabilities: capabilities, treeLimits: treeLimits, startedAt: startedAt,
		outcomePublished: make(chan struct{}),
		bookkeepingDone:  make(chan struct{}), joined: make(chan struct{}),
	}
}

func (p *processHandle) publishResult(result Result) bool {
	return p.publishOutcome(result, nil)
}

// Completion is monotonic. Repeated notifications leave the first published
// fact intact, and later boundaries cannot precede their prerequisites.
func (p *processHandle) publishOutcome(result Result, err *RuntimeError) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	select {
	case <-p.outcomePublished:
		return false
	default:
	}
	p.result, p.runtimeErr = result, err
	close(p.outcomePublished)
	return true
}

func (p *processHandle) finishBookkeeping() {
	p.mu.Lock()
	defer p.mu.Unlock()
	select {
	case <-p.outcomePublished:
	default:
		panic("agent: bookkeeping cannot precede outcome publication")
	}
	select {
	case <-p.bookkeepingDone:
		return
	default:
		close(p.bookkeepingDone)
	}
}

func (p *processHandle) finishJoin(err *RuntimeError) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	select {
	case <-p.bookkeepingDone:
	default:
		panic("agent: join cannot precede bookkeeping")
	}
	select {
	case <-p.joined:
		return false
	default:
	}
	p.joinErr = err
	close(p.joined)
	return true
}

func (p *processHandle) joinError() error {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.joinErr != nil {
		return p.joinErr.clone()
	}
	return nil
}

func (p *processHandle) joinDone() bool {
	select {
	case <-p.joined:
		return true
	default:
		return false
	}
}

func (p *processHandle) outcome() (Result, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.runtimeErr != nil {
		return Result{}, p.runtimeErr.clone()
	}
	return p.result, nil
}

func (p *processHandle) publishRuntimeFailure(err *RuntimeError) bool {
	return p.publishOutcome(Result{}, err)
}

func (p *processHandle) closedRequestError() error {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.runtimeErr != nil {
		return p.runtimeErr.clone()
	}
	return ErrProcessFinished
}
