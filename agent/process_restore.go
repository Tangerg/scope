package agent

import "fmt"

func prepareRestoredProcess(
	durable bool,
	deployment Deployment,
	snapshot ProcessSnapshot,
) (*processHandleState, *processState, processSnapshotWire, error) {
	wire, err := snapshot.wire()
	if err != nil {
		return nil, nil, processSnapshotWire{}, err
	}
	if wire.DeploymentRef != deployment.DeploymentRef() {
		return nil, nil, processSnapshotWire{}, fmt.Errorf(
			"%w: exact Deployment does not match", ErrInvalidSnapshot,
		)
	}
	if wire.Output != nil {
		if validateOutputErr := deployment.Descriptor().ValidateOutput(*wire.Output); validateOutputErr != nil {
			return nil, nil, processSnapshotWire{}, fmt.Errorf(
				"%w: output schema: %w", ErrInvalidSnapshot, validateOutputErr,
			)
		}
	}
	execution, err := restoreExecution(deployment.Definition(), wire.CommittedExecutionState)
	if err != nil {
		return nil, nil, processSnapshotWire{}, fmt.Errorf(
			"%w: restore Execution: %w", ErrInvalidSnapshot, err,
		)
	}
	mailbox, err := restoreSignalMailbox(wire.Mailbox, wire.Status)
	if err != nil {
		return nil, nil, processSnapshotWire{}, fmt.Errorf("%w: mailbox: %w", ErrInvalidSnapshot, err)
	}
	relation, err := processRelationFromWire(wire.ProcessID, wire.Relation)
	if err != nil {
		return nil, nil, processSnapshotWire{}, fmt.Errorf("%w: relation: %w", ErrInvalidSnapshot, err)
	}
	handle := newProcessHandleState(
		relation, wire.DeploymentRef, wire.Budget, wire.Capabilities, wire.TreeLimits,
		wire.StartedAt, wire.Status,
	)
	process, err := restoreProcessState(durable, handle, deployment, execution, mailbox, wire)
	if err != nil {
		return nil, nil, processSnapshotWire{}, err
	}
	return handle, process, wire, nil
}

func restoreProcessState(
	durable bool,
	handle *processHandleState,
	deployment Deployment,
	execution Execution,
	mailbox signalMailbox,
	wire processSnapshotWire,
) (*processState, error) {
	process := &processState{
		handle: handle, deployment: deployment, execution: execution,
		startedAt: wire.StartedAt, status: wire.Status, committedSteps: wire.CommittedSteps,
		committedExecutionState: wire.CommittedExecutionState, mailbox: mailbox, restored: true,
		pauseReason: wire.PauseReason, limits: wire.Limits, treeLimits: wire.TreeLimits,
		budget: wire.Budget, reservedBudget: wire.ReservedBudget,
		capabilities: wire.Capabilities, usage: wire.Usage,
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
	if wire.Output != nil {
		process.finalOutput = *wire.Output
	}
	if wire.Termination != nil {
		process.termination = *wire.Termination
	}
	control, err := pendingControlFromWire(wire.PendingControl)
	if err != nil {
		return nil, fmt.Errorf("%w: pending control: %w", ErrInvalidSnapshot, err)
	}
	process.pendingControl = control
	if err := process.restorePreparedStep(wire.Prepared, durable); err != nil {
		return nil, err
	}
	handle.updateStatus(process.status)
	return process, nil
}

func (p *processState) restorePreparedStep(stored *preparedStep, durable bool) error {
	if stored == nil {
		return nil
	}
	prepared := stored.snapshot()
	if output, completes := prepared.Transition.Output(); completes {
		if err := p.deployment.Descriptor().ValidateOutput(output); err != nil {
			return fmt.Errorf("%w: prepared output schema: %w", ErrInvalidSnapshot, err)
		}
	}
	var candidate Execution
	if !p.status.Terminal() {
		var err error
		candidate, err = restoreExecution(p.deployment.Definition(), prepared.CandidateState)
		if err != nil {
			return fmt.Errorf("%w: restore prepared Execution: %w", ErrInvalidSnapshot, err)
		}
	}
	for index := range prepared.Effects {
		record := &prepared.Effects[index]
		if err := p.deployment.validateEffect(record.Effect); err != nil {
			return fmt.Errorf("%w: prepared Effect: %w", ErrInvalidSnapshot, err)
		}
		if record.Phase != effectPhasePending {
			continue
		}
		policy := ReplayPolicyNever
		if record.Effect.Target() == EffectTargetFramework {
			operation, err := decodeFrameworkEffectOperation(record.Effect.Payload())
			if err != nil {
				return fmt.Errorf("%w: restore pending framework Effect: %w", ErrInvalidSnapshot, err)
			}
			if operation != frameworkEffectStartChild {
				continue
			}
		} else if !p.pendingControl.hasTerminalIntent() {
			var err error
			policy, err = dispatcherReplayPolicy(p.deployment.effectDispatcher(), record.Effect)
			if err != nil {
				return fmt.Errorf("%w: restore pending Effect: %w", ErrInvalidSnapshot, err)
			}
		}
		if record.Effect.Target() == EffectTargetDispatcher && !durable && policy == ReplayPolicyNever {
			if err := record.settleUnknown(); err != nil {
				return fmt.Errorf("%w: restore pending Effect: %w", ErrInvalidSnapshot, err)
			}
			continue
		}
		if p.restoredPending.id.Valid() {
			return fmt.Errorf("%w: multiple pending Effects", ErrInvalidSnapshot)
		}
		p.restoredPending = restoredPendingEffect{id: record.ID, replayPolicy: policy}
	}
	prepared.candidate = candidate
	p.prepared = &prepared
	return nil
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
		if err := validateTerminationReason(wire.PauseReason); err != nil {
			return pendingControl{}, err
		}
		control.pauseReason = wire.PauseReason
	}
	return control, nil
}
