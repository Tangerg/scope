package agent

import "fmt"

func prepareRestoredProcess(
	engine *Engine,
	deployment Deployment,
	snapshot ProcessSnapshot,
) (*processController, *processState, processSnapshotWire, error) {
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
	controller := newProcessController(
		relation, wire.DeploymentRef, wire.Budget, wire.Capabilities, wire.TreeLimits,
		wire.StartedAt, wire.Status,
	)
	process, err := restoreProcessState(engine, controller, deployment, execution, mailbox, wire)
	if err != nil {
		return nil, nil, processSnapshotWire{}, err
	}
	return controller, process, wire, nil
}

func restoreProcessState(
	engine *Engine,
	controller *processController,
	deployment Deployment,
	execution Execution,
	mailbox signalMailbox,
	wire processSnapshotWire,
) (*processState, error) {
	process := &processState{
		engine: engine, controller: controller, deployment: deployment, execution: execution,
		startedAt: wire.StartedAt, status: wire.Status, committedSteps: wire.CommittedSteps,
		processEventSequence: wire.ProcessEventSequence, committedExecutionState: wire.CommittedExecutionState, mailbox: mailbox, restored: true,
		pauseReason: wire.PauseReason, limits: wire.Limits, treeLimits: wire.TreeLimits,
		budget: wire.Budget, reservedBudget: wire.ReservedBudget,
		capabilities: wire.Capabilities, usage: wire.Usage,
	}
	if wire.ChildRequestDigest != nil {
		controller.childRequestDigest = *wire.ChildRequestDigest
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
	if err := process.restorePreparedStep(wire.Prepared); err != nil {
		return nil, err
	}
	controller.updateView(process.status, process.currentWaitID, process.usage)
	return process, nil
}

func (p *processState) restorePreparedStep(wire *preparedStepWire) error {
	if wire == nil {
		return nil
	}
	prepared := clonePreparedStep(*wire)
	if output, completes := prepared.Transition.Output(); completes {
		if err := p.deployment.Descriptor().ValidateOutput(output); err != nil {
			return fmt.Errorf("%w: prepared output schema: %w", ErrInvalidSnapshot, err)
		}
	}
	candidate, err := restoreExecution(p.deployment.Definition(), prepared.CandidateState)
	if err != nil {
		return fmt.Errorf("%w: restore prepared Execution: %w", ErrInvalidSnapshot, err)
	}
	for index := range prepared.Effects {
		record := &prepared.Effects[index]
		if err := p.deployment.validateEffect(record.Effect); err != nil {
			return fmt.Errorf("%w: prepared Effect: %w", ErrInvalidSnapshot, err)
		}
		if record.Phase != effectPhasePending || record.Effect.Target() != EffectTargetDispatcher {
			continue
		}
		policy, err := dispatcherReplayPolicy(p.deployment.effectDispatcher(), record.Effect)
		if err != nil {
			return fmt.Errorf("%w: restore pending Effect: %w", ErrInvalidSnapshot, err)
		}
		if p.engine.durability == nil && policy == ReplayPolicyNever {
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
	p.prepared = &preparedStep{wire: prepared, candidate: candidate}
	return nil
}

func (p *processState) capture() (ProcessSnapshot, error) {
	wire := processSnapshotWire{
		ProcessID:     p.controller.processID,
		Relation:      p.controller.relation.wire(),
		DeploymentRef: p.deployment.DeploymentRef(), StartedAt: p.startedAt,
		Status: p.status, CommittedSteps: p.committedSteps, ProcessEventSequence: p.processEventSequence,
		Limits: p.limits, TreeLimits: p.treeLimits,
		Budget: p.budget, ReservedBudget: p.reservedBudget,
		Capabilities: p.capabilities, Usage: p.usage,
		CommittedExecutionState: p.committedExecutionState, Mailbox: p.mailbox.snapshot(),
		PauseReason: p.pauseReason, PendingControl: p.pendingControl.wire(),
	}
	if p.controller.childRequestDigest.Valid() {
		digest := p.controller.childRequestDigest
		wire.ChildRequestDigest = &digest
	}
	if !p.finishedAt.IsZero() {
		finishedAt := p.finishedAt
		wire.FinishedAt = &finishedAt
	}
	if p.currentWaitID.Valid() {
		waitID := p.currentWaitID
		wire.CurrentWaitID = &waitID
	}
	if p.finalOutput.Valid() {
		output := p.finalOutput
		wire.Output = &output
	}
	if p.termination.Valid() {
		termination := p.termination
		wire.Termination = &termination
	}
	if p.prepared != nil {
		prepared := clonePreparedStep(p.prepared.wire)
		wire.Prepared = &prepared
	}
	return newProcessSnapshot(wire)
}

func (p *processState) result() Result {
	return Result{
		processID: p.controller.processID, startedAt: p.startedAt,
		finishedAt: p.finishedAt, output: p.finalOutput,
		termination: p.termination, usage: p.usage,
	}
}

func (p pendingControl) wire() pendingControlWire {
	wire := pendingControlWire{PauseReason: p.pauseReason}
	if p.failure.Valid() {
		failure := p.failure
		wire.Failure = &failure
	}
	if p.kill.valid() {
		wire.KillReason = p.kill.reason
	}
	if p.deadline.valid() {
		wire.DeadlineOwner = p.deadline.owner
		wire.DeadlineReason = p.deadline.reason
	}
	if p.cancellation.valid() {
		wire.CancellationOwner = p.cancellation.owner
		wire.CancellationReason = p.cancellation.reason
	}
	return wire
}

func pendingControlFromWire(wire pendingControlWire) (pendingControl, error) {
	if err := validatePendingControlWire(wire); err != nil {
		return pendingControl{}, err
	}
	control := pendingControl{pauseReason: wire.PauseReason}
	if wire.Failure != nil {
		control.failure = *wire.Failure
	}
	if wire.KillReason != "" {
		control.kill, _ = newKillIntent(wire.KillReason)
	}
	if wire.DeadlineOwner != "" {
		control.deadline, _ = newDeadlineIntent(wire.DeadlineOwner, wire.DeadlineReason)
	}
	if wire.CancellationOwner != "" {
		control.cancellation, _ = newCancellationIntent(wire.CancellationOwner, wire.CancellationReason)
	}
	return control, nil
}

func clonePreparedStep(value preparedStepWire) preparedStepWire {
	clone := value
	clone.Effects = make([]preparedEffectWire, len(value.Effects))
	for index, effect := range value.Effects {
		clone.Effects[index] = preparedEffectWire{
			ID: effect.ID, Effect: effect.Effect.clone(), Phase: effect.Phase,
		}
		if effect.WaitID != nil {
			waitID := *effect.WaitID
			clone.Effects[index].WaitID = &waitID
		}
		if effect.Settlement != nil {
			settlement := *effect.Settlement
			clone.Effects[index].Settlement = &settlement
		}
	}
	return clone
}
