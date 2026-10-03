package agent

import (
	"context"
	"fmt"

	"github.com/samber/lo"
)

func prepareRestoredProcess(
	ctx context.Context,
	deployment Deployment,
	snapshot ProcessSnapshot,
) (*processState, error) {
	wire, err := snapshot.wire()
	if err != nil {
		return nil, err
	}
	if wire.DeploymentRef != deployment.DeploymentRef() {
		return nil, fmt.Errorf(
			"%w: exact Deployment does not match", ErrInvalidSnapshot,
		)
	}
	if output := lo.FromPtr(wire.Finish).Output; output.Valid() {
		if validateOutputErr := deployment.Descriptor().ValidateOutput(output); validateOutputErr != nil {
			return nil, fmt.Errorf(
				"%w: output schema: %w", ErrInvalidSnapshot, validateOutputErr,
			)
		}
	}
	execution, err := restoreExecution(ctx, deployment.Definition(), wire.CommittedExecutionState)
	if err != nil {
		return nil, fmt.Errorf(
			"%w: restore Execution: %w", ErrInvalidSnapshot, err,
		)
	}
	mailbox, err := restoreSignalMailbox(wire.Mailbox, wire.status())
	if err != nil {
		return nil, fmt.Errorf("%w: mailbox: %w", ErrInvalidSnapshot, err)
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
			return nil, fmt.Errorf("%w: pending Signal: %w", ErrInvalidSnapshot, signalErr)
		}
	}
	handle := newProcessHandle(wire.Relation, deployment, wire.Budget, wire.Capabilities, wire.StartedAt)
	return restoreProcessState(ctx, handle, execution, mailbox, wire)
}

func restoreProcessState(
	ctx context.Context,
	handle *processHandle,
	execution Execution,
	mailbox signalMailbox,
	wire processSnapshotWire,
) (*processState, error) {
	process := &processState{
		handle: handle, execution: execution,
		committedSteps:          wire.CommittedSteps,
		committedExecutionState: wire.CommittedExecutionState, mailbox: mailbox, restored: true,
		counters: wire.Counters,
	}
	if wire.CurrentWaitID != nil {
		process.currentWaitID = *wire.CurrentWaitID
	}
	current, err := parsePause(wire.PauseReason)
	if err != nil {
		return nil, fmt.Errorf("%w: pause: %w", ErrInvalidSnapshot, err)
	}
	process.pause = current
	if wire.Finish != nil {
		process.finish = new(*wire.Finish)
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

// Each intent is absent only when all of its wire members are; a partial
// intent fails its constructor.
func pendingControlFromWire(wire pendingControlWire) (pendingControl, error) {
	var control pendingControl
	var err error
	if control.pause, err = parsePause(wire.PauseReason); err != nil {
		return pendingControl{}, err
	}
	if wire.Failure != nil {
		if !wire.Failure.Valid() {
			return pendingControl{}, ErrInvalidFailure
		}
		control.failure = *wire.Failure
	}
	if wire.KillReason != "" {
		if control.kill, err = newKillIntent(wire.KillReason); err != nil {
			return pendingControl{}, err
		}
	}
	if wire.Deadline != nil {
		if control.deadline, err = newDeadlineIntent(wire.Deadline.Owner, wire.Deadline.Reason); err != nil {
			return pendingControl{}, err
		}
	}
	if wire.Cancellation != nil {
		if control.cancellation, err = newCancellationIntent(wire.Cancellation.Owner, wire.Cancellation.Reason); err != nil {
			return pendingControl{}, err
		}
	}
	return control, nil
}
