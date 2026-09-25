package agent

import (
	"context"
	"fmt"
)

func prepareRestoredProcess(
	ctx context.Context,
	deployment Deployment,
	snapshot ProcessSnapshot,
) (*processHandle, *processState, processSnapshotWire, error) {
	wire, err := snapshot.wire()
	if err != nil {
		return nil, nil, processSnapshotWire{}, err
	}
	if wire.DeploymentRef != deployment.DeploymentRef() {
		return nil, nil, processSnapshotWire{}, fmt.Errorf(
			"%w: exact Deployment does not match", ErrInvalidSnapshot,
		)
	}
	if wire.Output.Valid() {
		if validateOutputErr := deployment.Descriptor().ValidateOutput(wire.Output); validateOutputErr != nil {
			return nil, nil, processSnapshotWire{}, fmt.Errorf(
				"%w: output schema: %w", ErrInvalidSnapshot, validateOutputErr,
			)
		}
	}
	execution, err := restoreExecution(ctx, deployment.Definition(), wire.CommittedExecutionState)
	if err != nil {
		return nil, nil, processSnapshotWire{}, fmt.Errorf(
			"%w: restore Execution: %w", ErrInvalidSnapshot, err,
		)
	}
	mailbox, err := restoreSignalMailbox(wire.Mailbox, wire.Status)
	if err != nil {
		return nil, nil, processSnapshotWire{}, fmt.Errorf("%w: mailbox: %w", ErrInvalidSnapshot, err)
	}
	for _, receipt := range snapshot.SignalReceipts() {
		signal, pending := receipt.PendingSignal()
		if !pending || signal.EngineOwned() {
			continue
		}
		if _, addressed := signal.WaitID(); addressed {
			continue
		}
		if signalErr := deployment.Descriptor().ValidateSignal(Payload{data: signal.Payload()}); signalErr != nil {
			return nil, nil, processSnapshotWire{}, fmt.Errorf("%w: pending Signal: %w", ErrInvalidSnapshot, signalErr)
		}
	}
	relation, err := processRelationFromWire(wire.ProcessID, wire.Relation)
	if err != nil {
		return nil, nil, processSnapshotWire{}, fmt.Errorf("%w: relation: %w", ErrInvalidSnapshot, err)
	}
	handle := newProcessHandle(
		relation, wire.DeploymentRef, wire.Limits.Budget, wire.Capabilities, wire.TreeLimits,
		wire.StartedAt)
	process, err := restoreProcessState(ctx, handle, deployment, execution, mailbox, wire)
	if err != nil {
		return nil, nil, processSnapshotWire{}, err
	}
	return handle, process, wire, nil
}

func restoreProcessState(
	ctx context.Context,
	handle *processHandle,
	deployment Deployment,
	execution Execution,
	mailbox signalMailbox,
	wire processSnapshotWire,
) (*processState, error) {
	process := &processState{
		handle: handle, deployment: deployment, execution: execution,
		startedAt: wire.StartedAt, status: wire.Status, committedSteps: wire.CommittedSteps,
		committedExecutionState: wire.CommittedExecutionState, mailbox: mailbox, restored: true,
		pauseReason: wire.PauseReason, treeLimits: wire.TreeLimits,
		allocatedResources: wire.AllocatedResources,
		capabilities:       wire.Capabilities, counters: wire.Counters, limits: wire.Limits,
	}
	if wire.ChildRequestDigest != nil {
		handle.childRequestDigest = *wire.ChildRequestDigest
	}
	if wire.FinishedAt != nil {
		process.finishedAt = *wire.FinishedAt
	}
	if wire.CurrentWaitID != nil {
		process.currentWaitID = *wire.CurrentWaitID
	}
	process.finalOutput = wire.Output
	if wire.Termination != nil {
		process.termination = *wire.Termination
	}
	control, err := pendingControlFromWire(wire.PendingControl)
	if err != nil {
		return nil, fmt.Errorf("%w: pending control: %w", ErrInvalidSnapshot, err)
	}
	process.pendingControl = control
	if err := process.restorePreparedStep(ctx, wire.Prepared); err != nil {
		return nil, err
	}
	return process, nil
}

func pendingControlFromWire(wire pendingControlWire) (pendingControl, error) {
	if wire.Failure != nil && !wire.Failure.Valid() {
		return pendingControl{}, ErrInvalidFailure
	}
	var control pendingControl
	if wire.Failure != nil {
		control.failure = *wire.Failure
	}
	if wire.KillReason != "" {
		kill, err := newKillIntent(wire.KillReason)
		if err != nil {
			return pendingControl{}, err
		}
		control.kill = kill
	}
	if (wire.DeadlineOwner == "") != (wire.DeadlineReason == "") {
		return pendingControl{}, errInvalidTermination
	}
	if wire.DeadlineOwner != "" {
		deadline, err := newDeadlineIntent(wire.DeadlineOwner, wire.DeadlineReason)
		if err != nil {
			return pendingControl{}, err
		}
		control.deadline = deadline
	}
	if (wire.CancellationOwner == "") != (wire.CancellationReason == "") {
		return pendingControl{}, errInvalidTermination
	}
	if wire.CancellationOwner != "" {
		cancellation, err := newCancellationIntent(wire.CancellationOwner, wire.CancellationReason)
		if err != nil {
			return pendingControl{}, err
		}
		control.cancellation = cancellation
	}
	if wire.PauseReason != "" {
		if !validPauseReason(wire.PauseReason) {
			return pendingControl{}, fmt.Errorf("pause reason must be non-empty, trimmed UTF-8 within %d bytes", maxPauseReasonBytes)
		}
		control.pauseReason = wire.PauseReason
	}
	return control, nil
}
