package agent

import (
	"context"
	"errors"
	"slices"
)

// TreeFreezePhase describes the current scheduling barrier. Acquiring blocks
// scheduling while in-flight Effects settle. Held also stops completion
// adoption; an in-flight Step may still be computing against its isolated state.
type TreeFreezePhase string

const (
	TreeFreezeNone      TreeFreezePhase = "none"
	TreeFreezeAcquiring TreeFreezePhase = "acquiring"
	TreeFreezeHeld      TreeFreezePhase = "held"
)

// ProcessWork describes work owned by the current runtime, independently of
// the lifecycle state in its last acknowledged snapshot. Queued means waiting
// for an owner turn; a commit or freeze can still block it. Idle means no queued
// work or job, not a terminal Process or a successful execution.
type ProcessWork string

const (
	ProcessWorkIdle       ProcessWork = "idle"
	ProcessWorkQueued     ProcessWork = "queued"
	ProcessWorkStep       ProcessWork = "step"
	ProcessWorkDispatch   ProcessWork = "dispatch"
	ProcessWorkChildStart ProcessWork = "child_start"
)

// ProcessInspection combines an existing execution capture with current work.
// A stale job is still draining but cannot publish its result. RuntimeError
// reports an instance failure; its unresolved Effects may exceed the Unknown
// settlements already present in Snapshot.
type ProcessInspection struct {
	Snapshot     ProcessSnapshot
	Work         ProcessWork
	Stale        bool
	EffectID     EffectID
	RuntimeError *RuntimeError
}

// TreeInspection is a caller-owned report from one runtime owner turn. In
// durable mode, snapshots and HeadDigest come only from the last head this
// instance acknowledged. A lost response or a replacement writer may have
// advanced storage further. Recovery must load the authoritative stored tree.
// IncarnationID and HeadDigest are zero in ephemeral mode. Work and barriers
// describe the sampling turn and may be newer than the acknowledged snapshots.
// Stopped means the owner has exited after draining its work. Reports contain
// no recovery or scheduling authority and are not a persistence schema.
type TreeInspection struct {
	RootID        ProcessID
	IncarnationID TreeIncarnationID
	HeadDigest    Digest
	CommitPending bool
	Freeze        TreeFreezePhase
	Stopped       bool
	Processes     []ProcessInspection
}

// Process finds a published Process in the report's canonical depth/ID order.
func (t TreeInspection) Process(processID ProcessID) (ProcessInspection, bool) {
	for _, process := range t.Processes {
		if process.Snapshot.ProcessID() == processID {
			return process, true
		}
	}
	return ProcessInspection{}, false
}

func (t TreeInspection) clone() TreeInspection {
	t.Processes = slices.Clone(t.Processes)
	for index := range t.Processes {
		t.Processes[index].RuntimeError = t.Processes[index].RuntimeError.clone()
	}
	return t
}

// InspectTree samples a registered root tree without freezing it or waiting
// for storage acknowledgment. It remains available during a commit, while a
// freeze is acquired or held, and after the owner stops or Engine.Close returns.
// ReleaseTree removes the lookup; an overlapping inspection may return its
// earlier valid sample. ctx bounds both admission and response waiting.
// The owner never calls user execution code to construct a report. Dependencies
// must still return in bounded time: synchronous EventListeners must not query
// or control their own tree. Query failures are returned as errors; runtime
// failures are reported through ProcessInspection.RuntimeError.
func (e *Engine) InspectTree(ctx context.Context, rootID ProcessID) (TreeInspection, error) {
	if e == nil {
		return TreeInspection{}, ErrEngineClosed
	}
	ctx = requireContext(ctx)
	if err := ctx.Err(); err != nil {
		return TreeInspection{}, err
	}
	runtime, err := e.runtimeForTree(rootID)
	if err != nil {
		return TreeInspection{}, err
	}
	return runtime.inspect(ctx)
}

type treeInspectionResponse struct {
	inspection TreeInspection
	err        error
}

func (t *treeRuntime) inspect(ctx context.Context) (TreeInspection, error) {
	select {
	case <-t.done:
		return t.finalInspection.inspection.clone(), t.finalInspection.err
	default:
	}
	response := make(chan treeInspectionResponse, 1)
	select {
	case t.inspections <- response:
	case <-t.done:
		return t.finalInspection.inspection.clone(), t.finalInspection.err
	case <-ctx.Done():
		return TreeInspection{}, ctx.Err()
	}
	select {
	case result := <-response:
		return result.inspection, result.err
	case <-t.done:
		return t.finalInspection.inspection.clone(), t.finalInspection.err
	case <-ctx.Done():
		return TreeInspection{}, ctx.Err()
	}
}

func (t *treeRuntime) tryInspection() bool {
	select {
	case response := <-t.inspections:
		t.replyInspection(response)
		return true
	default:
		return false
	}
}

func (t *treeRuntime) replyInspection(response chan treeInspectionResponse) {
	inspection, err := t.buildInspection()
	response <- treeInspectionResponse{inspection: inspection, err: err}
}

func (t *treeRuntime) buildInspection() (TreeInspection, error) {
	inspection := TreeInspection{
		RootID: t.rootID, IncarnationID: t.incarnation, HeadDigest: t.head.digest(),
		CommitPending: t.commit != nil, Freeze: TreeFreezeNone,
	}
	if t.freeze != nil {
		inspection.Freeze = TreeFreezeAcquiring
		if t.freeze.ready {
			inspection.Freeze = TreeFreezeHeld
		}
	}
	var snapshots []ProcessSnapshot
	if t.head != nil {
		snapshots = t.head.snapshot.ProcessSnapshots()
	} else {
		for _, process := range t.processesInCanonicalOrder() {
			snapshot, err := process.capture()
			if err != nil {
				return TreeInspection{}, err
			}
			snapshots = append(snapshots, snapshot)
		}
	}
	for _, snapshot := range snapshots {
		processID := snapshot.ProcessID()
		process := t.processes[processID]
		if process == nil {
			continue
		}
		report := ProcessInspection{Snapshot: snapshot, Work: ProcessWorkIdle}
		_, runtimeErr := process.handle.outcome()
		report.RuntimeError, _ = errors.AsType[*RuntimeError](runtimeErr)
		if job := t.jobs[processID]; job != nil {
			report.Stale = job.stale
			report.EffectID = job.effectID
			switch job.kind {
			case processJobStep:
				report.Work = ProcessWorkStep
			case processJobDispatch:
				report.Work = ProcessWorkDispatch
			case processJobChildStart:
				report.Work = ProcessWorkChildStart
			}
		} else if _, queued := t.queued[processID]; queued && !process.status.Terminal() {
			report.Work = ProcessWorkQueued
		}
		inspection.Processes = append(inspection.Processes, report)
	}
	return inspection, nil
}
