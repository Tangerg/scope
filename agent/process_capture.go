package agent

func (p *processState) capture() (ProcessSnapshot, error) {
	wire := processSnapshotWire{
		ProcessID:     p.handle.processID,
		Relation:      p.handle.relation.wire(),
		DeploymentRef: p.deployment.DeploymentRef(), StartedAt: p.startedAt,
		Status: p.status, CommittedSteps: p.committedSteps, ProcessEventSequence: p.processEventSequence,
		Limits: p.limits, TreeLimits: p.treeLimits,
		Budget: p.budget, ReservedBudget: p.reservedBudget,
		Capabilities: p.capabilities, Usage: p.usage,
		CommittedExecutionState: p.committedExecutionState, Mailbox: p.mailbox.snapshot(),
		PauseReason: p.pauseReason, PendingControl: p.pendingControl.wire(),
	}
	if p.handle.childRequestDigest.Valid() {
		digest := p.handle.childRequestDigest
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
		prepared := clonePreparedStepWire(p.prepared.wire)
		wire.Prepared = &prepared
	}
	return newProcessSnapshot(wire)
}

func (p *processState) result() Result {
	return Result{
		processID: p.handle.processID, startedAt: p.startedAt,
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

func clonePreparedStepWire(value preparedStepWire) preparedStepWire {
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
