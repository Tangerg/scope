package agent

import (
	"fmt"
	"slices"
	"time"
)

func projectWaitingSubtreeCancellation(
	source TreeSnapshot,
	targetID ProcessID,
	reason string,
	finishedAt time.Time,
) (TreeSnapshot, []ProcessID, []ProcessID, error) {
	projection, err := newWaitingSubtreeProjection(source, targetID, finishedAt)
	if err != nil {
		return TreeSnapshot{}, nil, nil, err
	}
	if err := projection.cancelActiveSubtree(reason); err != nil {
		return TreeSnapshot{}, nil, nil, err
	}
	if err := projection.reconcileChildWaits(); err != nil {
		return TreeSnapshot{}, nil, nil, err
	}
	return projection.result()
}

type waitingSubtreeProjection struct {
	tree        treeSnapshotWire
	processes   map[ProcessID]processSnapshotWire
	indexes     map[ProcessID]int
	targetID    ProcessID
	finishedAt  time.Time
	canceled    []ProcessID
	canceledSet map[ProcessID]struct{}
	pausedSet   map[ProcessID]struct{}
}

func newWaitingSubtreeProjection(
	source TreeSnapshot,
	targetID ProcessID,
	finishedAt time.Time,
) (*waitingSubtreeProjection, error) {
	wire, err := source.wire()
	if err != nil {
		return nil, err
	}
	processes, indexes, err := processSnapshotWires(wire.ProcessSnapshots)
	if err != nil {
		return nil, err
	}
	target, exists := processes[targetID]
	if !exists || targetID == wire.RootID || target.Status != StatusWaiting ||
		target.Prepared != nil || !emptyPendingControl(target.PendingControl) {
		return nil, ErrWaitingSubtreeCancellationUnavailable
	}
	return &waitingSubtreeProjection{
		tree: wire, processes: processes, indexes: indexes, targetID: targetID,
		finishedAt: finishedAt, canceledSet: make(map[ProcessID]struct{}),
		pausedSet: make(map[ProcessID]struct{}),
	}, nil
}

func (w *waitingSubtreeProjection) cancelActiveSubtree(reason string) error {
	for _, snapshot := range w.tree.ProcessSnapshots {
		candidate := w.processes[snapshot.ProcessID()]
		if !candidate.Status.Terminal() && w.belongsToTarget(candidate.ProcessID) {
			if candidate.Prepared != nil || !emptyPendingControl(candidate.PendingControl) {
				return ErrWaitingSubtreeCancellationUnavailable
			}
			w.canceled = append(w.canceled, candidate.ProcessID)
		}
	}
	if len(w.canceled) == 0 {
		return ErrWaitingSubtreeCancellationUnavailable
	}
	for _, id := range w.canceled {
		w.canceledSet[id] = struct{}{}
		intentOwner := cancellationOwnerParent
		terminationReason := "parent Process reached terminal status canceled"
		if id == w.targetID {
			intentOwner = cancellationOwnerHost
			terminationReason = reason
		}
		intent, err := newCancellationIntent(intentOwner, terminationReason)
		if err != nil {
			return err
		}
		termination, err := resolveTermination(terminationFacts{cancellation: intent})
		if err != nil {
			return err
		}
		if err := w.cancelProcess(id, termination); err != nil {
			return err
		}
	}
	return nil
}

func (w *waitingSubtreeProjection) cancelProcess(
	processID ProcessID,
	termination Termination,
) error {
	process := w.processes[processID]
	mailbox, err := restoreSignalMailbox(process.Mailbox)
	if err != nil || process.ProcessEventSequence == ^uint64(0) {
		return ErrWaitingSubtreeCancellationUnavailable
	}
	mailbox.closeAllWaits()
	finishedAt := w.finishedAt
	process.Status = StatusCanceled
	process.FinishedAt = &finishedAt
	process.ProcessEventSequence++
	process.Mailbox = mailbox.snapshot()
	process.Prepared = nil
	process.CurrentWaitID = nil
	process.PauseReason = ""
	process.PendingControl = pendingControlWire{}
	process.Output = nil
	process.Termination = &termination
	return w.replaceProcess(process)
}

func (w *waitingSubtreeProjection) reconcileChildWaits() error {
	surviving := make([]childWaitSnapshotWire, 0, len(w.tree.ChildWaits))
	for _, wait := range w.tree.ChildWaits {
		if _, canceledParent := w.canceledSet[wait.ParentProcessID]; canceledParent {
			continue
		}
		surviving = append(surviving, wait)
		if !w.childWaitTouchesCanceledProcess(wait.Spec) {
			continue
		}
		if err := w.deliverBoundaryCompletion(wait); err != nil {
			return err
		}
	}
	w.tree.ChildWaits = surviving
	return nil
}

func (w *waitingSubtreeProjection) deliverBoundaryCompletion(
	wait childWaitSnapshotWire,
) error {
	spec, err := wait.Spec.value()
	if err != nil {
		return err
	}
	outcomes, ready, err := w.childOutcomes(spec)
	if err != nil {
		return err
	}
	if !ready {
		return nil
	}
	signal, err := encodeChildrenCompleted(wait.WaitID, spec.Key, outcomes)
	if err != nil {
		return err
	}
	parent := w.processes[wait.ParentProcessID]
	if parent.Status.Terminal() || parent.Prepared != nil || !emptyPendingControl(parent.PendingControl) {
		return ErrWaitingSubtreeCancellationUnavailable
	}
	mailbox, err := restoreSignalMailbox(parent.Mailbox)
	if err != nil {
		return err
	}
	if !mailbox.contains(signal.ID()) {
		if !resourceQuantitiesFit(parent.Limits.MaxSignals, parent.Usage.AcceptedSignals, 1) ||
			!resourceQuantitiesFit(parent.Limits.MaxPendingSignals, mailbox.pendingCount(), 1) ||
			!resourceQuantitiesFit(
				parent.Budget.Signals,
				parent.Usage.AcceptedSignals, parent.ReservedBudget.Signals, 1,
			) {
			return fmt.Errorf(
				"%w: %w", ErrWaitingSubtreeCancellationUnavailable, ErrResourceLimitExceeded,
			)
		}
		accepted, err := mailbox.enqueue(parent.Status, signal, signalSourceChildCompletion)
		if err != nil {
			return err
		}
		if !accepted || parent.ProcessEventSequence == ^uint64(0) {
			return ErrWaitingSubtreeCancellationUnavailable
		}
		parent.Usage.AcceptedSignals++
		parent.ProcessEventSequence++
	}
	if parent.Status == StatusRunning || parent.Status == StatusWaiting {
		if parent.ProcessEventSequence == ^uint64(0) {
			return ErrWaitingSubtreeCancellationUnavailable
		}
		parent.Status = StatusPaused
		parent.CurrentWaitID = nil
		parent.PauseReason = waitingSubtreeParentPauseReason
		parent.PendingControl = pendingControlWire{}
		parent.ProcessEventSequence++
		w.pausedSet[parent.ProcessID] = struct{}{}
	}
	parent.Mailbox = mailbox.snapshot()
	return w.replaceProcess(parent)
}

func (w *waitingSubtreeProjection) result() (
	TreeSnapshot,
	[]ProcessID,
	[]ProcessID,
	error,
) {
	result, err := newTreeSnapshot(w.tree)
	if err != nil {
		return TreeSnapshot{}, nil, nil, err
	}
	paused := make([]ProcessID, 0, len(w.pausedSet))
	for _, snapshot := range result.ProcessSnapshots() {
		if _, exists := w.pausedSet[snapshot.ProcessID()]; exists {
			paused = append(paused, snapshot.ProcessID())
		}
	}
	return result, slices.Clone(w.canceled), paused, nil
}

func processSnapshotWires(
	snapshots []ProcessSnapshot,
) (map[ProcessID]processSnapshotWire, map[ProcessID]int, error) {
	wires := make(map[ProcessID]processSnapshotWire, len(snapshots))
	indexes := make(map[ProcessID]int, len(snapshots))
	for index, snapshot := range snapshots {
		wire, err := snapshot.wire()
		if err != nil {
			return nil, nil, err
		}
		wires[wire.ProcessID] = wire
		indexes[wire.ProcessID] = index
	}
	return wires, indexes, nil
}

func (w *waitingSubtreeProjection) replaceProcess(process processSnapshotWire) error {
	snapshot, err := newProcessSnapshot(process)
	if err != nil {
		return err
	}
	w.processes[process.ProcessID] = process
	w.tree.ProcessSnapshots[w.indexes[process.ProcessID]] = snapshot
	return nil
}

func (w *waitingSubtreeProjection) belongsToTarget(processID ProcessID) bool {
	for processID.Valid() {
		if processID == w.targetID {
			return true
		}
		process, exists := w.processes[processID]
		if !exists {
			return false
		}
		relation, err := processRelationFromWire(processID, process.Relation)
		if err != nil {
			return false
		}
		parentID, child := relation.ParentID()
		if !child {
			return false
		}
		processID = parentID
	}
	return false
}

func (w *waitingSubtreeProjection) childWaitTouchesCanceledProcess(
	spec childWaitSpecWire,
) bool {
	for _, childID := range spec.Children {
		if _, exists := w.canceledSet[childID]; exists {
			return true
		}
	}
	return false
}

func (w *waitingSubtreeProjection) childOutcomes(
	spec ChildWaitSpec,
) ([]ChildOutcome, bool, error) {
	outcomes := make([]ChildOutcome, 0, len(spec.Children))
	for _, childID := range spec.Children {
		process, exists := w.processes[childID]
		if !exists {
			return nil, false, ErrInvalidChildWait
		}
		if !process.Status.Terminal() {
			continue
		}
		relation, err := processRelationFromWire(process.ProcessID, process.Relation)
		if err != nil {
			return nil, false, err
		}
		key, child := relation.ChildKey()
		if !child {
			return nil, false, ErrInvalidChildWait
		}
		result := resultFromProcessSnapshotWire(process)
		if !result.Valid() {
			return nil, false, ErrInvalidChildWait
		}
		outcomes = append(outcomes, ChildOutcome{key: key, result: result})
	}
	required, err := spec.Condition.required(len(spec.Children))
	return outcomes, err == nil && uint32(len(outcomes)) >= required, err
}

func resultFromProcessSnapshotWire(process processSnapshotWire) Result {
	var output Output
	if process.Output != nil {
		output = *process.Output
	}
	var termination Termination
	if process.Termination != nil {
		termination = *process.Termination
	}
	var finishedAt time.Time
	if process.FinishedAt != nil {
		finishedAt = *process.FinishedAt
	}
	return Result{
		processID: process.ProcessID, startedAt: process.StartedAt, finishedAt: finishedAt,
		output: output, termination: termination, usage: process.Usage,
	}
}
