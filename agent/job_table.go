package agent

import (
	"context"
	"iter"
	"maps"
	"sync/atomic"
)

type processAttempt uint64

type processJobKind uint8

const (
	processJobInvalid processJobKind = iota
	processJobStep
	processJobRestore
	processJobDispatch
	processJobChildStart
)

// work is the inspection vocabulary for a job of this kind.
func (p processJobKind) work() ProcessWork {
	switch p {
	case processJobStep:
		return ProcessWorkStep
	case processJobRestore:
		return ProcessWorkRestore
	case processJobDispatch:
		return ProcessWorkDispatch
	case processJobChildStart:
		return ProcessWorkChildStart
	default:
		return ProcessWorkInvalid
	}
}

type processJob struct {
	kind          processJobKind
	attempt       processAttempt
	cancel        context.CancelFunc
	stale         bool
	effectID      EffectID
	childStart    *childStartPlan
	effectAttempt effectAttempt
	reply         processReply
}

// computesCandidate reports whether the job only computes a candidate. Such work
// can be discarded; dispatch and child admission may already have external
// effects that must settle.
func (p *processJob) computesCandidate() bool {
	return p.kind == processJobStep || p.kind == processJobRestore
}

func (p *processJob) cancelContext() {
	if p.cancel != nil {
		p.cancel()
	}
}

// interrupt cancels the job and discards its result when it computes a candidate;
// external work keeps its result so the outcome it produced is still settled.
func (p *processJob) interrupt() {
	if p.computesCandidate() {
		p.stale = true
	}
	p.cancelContext()
}

// abandon discards the result of any kind of job; the writer has stopped and
// can no longer adopt it.
func (p *processJob) abandon() {
	p.stale = true
	p.cancelContext()
}

// uncertainEffect names the Effect whose external outcome this job may have
// produced without reporting it.
func (p *processJob) uncertainEffect() (EffectID, bool) {
	return p.effectID, !p.computesCandidate() && p.effectID.Valid()
}

type treeJobCompletion struct {
	processID ProcessID
	attempt   processAttempt
	result    jobResult
}

// jobResult is the outcome of one owned job. Its type names the job kind, so a
// completion cannot carry the result of a different kind of work.
type jobResult interface{ jobKind() processJobKind }

// jobTable owns the work each Process has in flight: at most one job per
// Process, retired only by a completion matching its kind and attempt. active
// mirrors the table size for lock-free Engine.Close checks.
type jobTable struct {
	jobs   map[ProcessID]*processJob
	active atomic.Int64
}

func newJobTable(capacity int) *jobTable {
	return &jobTable{jobs: make(map[ProcessID]*processJob, capacity)}
}

func (j *jobTable) get(processID ProcessID) *processJob { return j.jobs[processID] }

func (j *jobTable) empty() bool { return len(j.jobs) == 0 }

func (j *jobTable) start(processID ProcessID, job *processJob) {
	if !processID.Valid() || job == nil || j.jobs[processID] != nil {
		panic("agent: invalid concurrent Process job")
	}
	j.jobs[processID] = job
	j.active.Add(1)
}

func (j *jobTable) matching(completion treeJobCompletion) (*processJob, bool) {
	job := j.jobs[completion.processID]
	if job == nil || job.kind != completion.result.jobKind() || job.attempt != completion.attempt {
		return nil, false
	}
	return job, true
}

// finish retires the job that completion answers and cancels its context.
// Completions of retired or superseded attempts are ignored.
func (j *jobTable) finish(completion treeJobCompletion) (*processJob, bool) {
	job, current := j.matching(completion)
	if !current {
		return nil, false
	}
	delete(j.jobs, completion.processID)
	j.active.Add(-1)
	job.cancelContext()
	return job, true
}

func (j *jobTable) all() iter.Seq2[ProcessID, *processJob] { return maps.All(j.jobs) }

// hasExternal reports work whose external effects must settle before the
// tree can freeze.
func (j *jobTable) hasExternal() bool {
	for _, job := range j.jobs {
		if !job.computesCandidate() {
			return true
		}
	}
	return false
}
