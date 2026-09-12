package agent

import (
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
	ProcessWorkRestore    ProcessWork = "restore"
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

type treeInspectionResponse struct {
	inspection TreeInspection
	err        error
}
