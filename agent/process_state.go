package agent

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"
)

type processState struct {
	// These references establish ownership and remain fixed while the runtime
	// owner goroutine mutates the execution fields below.
	handle     *processHandleState
	deployment Deployment

	// Only treeRuntime's owner goroutine mutates protocol and recovery state, keeping
	// snapshots and externally visible transitions in one deterministic order.
	execution               Execution
	preparedExecution       Execution
	startedAt               time.Time
	finishedAt              time.Time
	status                  Status
	committedSteps          uint64
	processEventSequence    uint64
	committedExecutionState ExecutionState
	mailbox                 signalMailbox
	prepared                *preparedStep
	currentWaitID           WaitID
	pauseReason             string
	pendingControl          pendingControl
	finalOutput             Output
	termination             Termination
	snapshot                ProcessSnapshot
	snapshotDrained         bool

	// Allocation and authority stay adjacent because every child reservation
	// must update both before it can become observable.
	limits                 Limits
	treeLimits             TreeLimits
	budget                 Budget
	reservedBudget         Budget
	provisionalChildBudget Budget
	capabilities           CapabilitySet
	usage                  Usage

	// Restore bookkeeping is consumed by the owner goroutine before admitting
	// new work, preventing recovered effects from racing fresh execution.
	restored        bool
	restoredPending restoredPendingEffect
	attemptSequence uint64
}

type restoredPendingEffect struct {
	id           EffectID
	replayPolicy ReplayPolicy
}

func (r restoredPendingEffect) matches(effectID EffectID) bool {
	return r.id.Valid() && r.id == effectID && r.replayPolicy.Valid()
}

type pendingControl struct {
	failure      Failure
	kill         killIntent
	deadline     deadlineIntent
	cancellation cancellationIntent
	pauseReason  string
}

func newProcessState(
	handle *processHandleState,
	deployment Deployment,
	execution Execution,
	state ExecutionState,
	startedAt time.Time,
	limits Limits,
) *processState {
	return &processState{
		handle: handle, deployment: deployment, execution: execution,
		startedAt: startedAt, status: StatusRunning, committedExecutionState: state,
		mailbox: newSignalMailbox(), limits: limits, treeLimits: handle.treeLimits,
		budget: handle.budget, capabilities: handle.capabilities,
	}
}

func (p *processState) recordHostTermination(err error) {
	if errors.Is(err, context.DeadlineExceeded) {
		intent, _ := newDeadlineIntent(deadlineOwnerHost, "host context deadline reached")
		if !p.pendingControl.deadline.valid() {
			p.pendingControl.deadline = intent
		}
		return
	}
	intent, _ := newCancellationIntent(cancellationOwnerHost, "host context canceled")
	if !p.pendingControl.cancellation.valid() {
		p.pendingControl.cancellation = intent
	}
}

func (p *processState) recordParentTermination(parent Termination) {
	if !parent.Valid() {
		return
	}
	if parent.Status() == StatusTimedOut {
		intent, _ := newDeadlineIntent(deadlineOwnerParent, "parent Process reached a deadline")
		if !p.pendingControl.deadline.valid() {
			p.pendingControl.deadline = intent
		}
		return
	}
	intent, _ := newCancellationIntent(
		cancellationOwnerParent,
		"parent Process reached terminal status "+parent.Status().String(),
	)
	if !p.pendingControl.cancellation.valid() {
		p.pendingControl.cancellation = intent
	}
}

// Admission validates the complete batch before changing mailbox or wait state.
func (p *processState) admitSignals(signals []Signal, source signalSource) (bool, error) {
	admission, err := p.mailbox.prepareAdmission(p.status, p.currentWaitID, signals, source)
	if err != nil {
		return false, err
	}
	if admission.duplicate {
		return false, nil
	}
	count := uint64(len(signals))
	reserved := p.prepared.settlementSignalCount()
	remainingPending := p.mailbox.pendingCount()
	remainingPending -= p.prepared.consumedSignals()
	reservedBudget := p.effectiveReservedBudget()
	if !resourceQuantitiesFit(p.limits.MaxSignals, p.usage.AcceptedSignals, reserved, count) ||
		!resourceQuantitiesFit(p.limits.MaxPendingSignals, p.mailbox.pendingCount(), count) ||
		!resourceQuantitiesFit(p.limits.MaxPendingSignals, remainingPending, reserved, count) ||
		!resourceQuantitiesFit(p.budget.Signals, p.usage.AcceptedSignals, reservedBudget.Signals, reserved, count) {
		return false, ErrResourceLimitExceeded
	}
	for _, record := range admission.records {
		p.mailbox.acceptRecord(record)
	}
	if p.status == StatusWaiting && admission.status == StatusRunning {
		p.status = StatusRunning
		p.currentWaitID = WaitID{}
	}
	p.usage.AcceptedSignals += count
	return true, nil
}

func (p *processState) requestPause(reason string) error {
	if err := validateTerminationReason(reason); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidProcessControl, err)
	}
	if p.status != StatusRunning {
		return ErrProcessNotRunning
	}
	if p.pendingControl.pauseReason == "" {
		p.pendingControl.pauseReason = reason
	}
	return nil
}

func (p *processState) applyPendingPause() bool {
	if p.pendingControl.pauseReason == "" || p.status != StatusRunning {
		return false
	}
	p.status = StatusPaused
	p.pauseReason = p.pendingControl.pauseReason
	p.pendingControl.pauseReason = ""
	return true
}

func (p *processState) resume() error {
	if p.status != StatusPaused {
		return fmt.Errorf("%w: Resume requires Paused status, got %s", ErrInvalidProcessControl, p.status)
	}
	p.status = StatusRunning
	p.pauseReason = ""
	p.pendingControl.pauseReason = ""
	return nil
}

func (p *processState) requestCancellation(intent cancellationIntent) {
	if !p.pendingControl.cancellation.valid() {
		p.pendingControl.cancellation = intent
	}
}

func (p *processState) requestKill(reason string) error {
	intent, err := newKillIntent(reason)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidProcessControl, err)
	}
	if !p.pendingControl.kill.valid() {
		p.pendingControl.kill = intent
	}
	return nil
}

func (p *processState) resolveEffect(settlement Settlement) error {
	_, _, err := p.prepared.resolveUnknown(settlement)
	return err
}

func (p *processState) unknownEffectIDs() []EffectID {
	if p.prepared == nil {
		return nil
	}
	return p.prepared.Effects.unknownEffectIDs()
}

func (p *processState) reserveProvisionalChildBudget(requested Budget) bool {
	if p.provisionalChildBudget.Valid() {
		return false
	}
	reserved, ok := p.reservedBudget.add(Budget{
		Steps: 1, Signals: p.prepared.settlementSignalCount(),
	})
	if !ok || !p.budget.canAllocate(p.usage, reserved, requested) {
		return false
	}
	p.provisionalChildBudget = requested
	return true
}

func (p *processState) commitProvisionalChildBudget(requested Budget) error {
	if !requested.Valid() || p.provisionalChildBudget != requested {
		return ErrResourceLimitExceeded
	}
	reserved, ok := p.reservedBudget.add(requested)
	if !ok {
		return ErrResourceLimitExceeded
	}
	p.reservedBudget = reserved
	p.provisionalChildBudget = Budget{}
	return nil
}

func (p *processState) releaseProvisionalChildBudget(requested Budget) {
	if p.provisionalChildBudget == requested {
		p.provisionalChildBudget = Budget{}
	}
}

func (p *processState) releaseCommittedChildBudget(released Budget) {
	if released.Steps > p.reservedBudget.Steps ||
		released.Effects > p.reservedBudget.Effects ||
		released.Signals > p.reservedBudget.Signals {
		return
	}
	p.reservedBudget.Steps -= released.Steps
	p.reservedBudget.Effects -= released.Effects
	p.reservedBudget.Signals -= released.Signals
}

func (p *processState) effectiveReservedBudget() Budget {
	reserved, ok := p.reservedBudget.add(p.provisionalChildBudget)
	if !ok {
		panic("agent: Process resource reservation overflow")
	}
	return reserved
}

func (p *processState) capture() (ProcessSnapshot, error) {
	if p.snapshotDrained {
		return p.snapshot, nil
	}
	wire := processSnapshotWire{
		ProcessID:     p.handle.processID,
		Relation:      p.handle.relation.wire(),
		DeploymentRef: p.deployment.DeploymentRef(), StartedAt: p.startedAt,
		Status: p.status, CommittedSteps: p.committedSteps,
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
		prepared := p.prepared.snapshot()
		wire.Prepared = &prepared
	}
	// Compare every persisted fact, including mailbox and Effect contents. This
	// avoids a second mutation protocol whose invalidation could miss a control,
	// reservation, or settlement while reusing the already validated value.
	if p.snapshot.state != nil && reflect.DeepEqual(wire, *p.snapshot.state) {
		p.snapshotDrained = p.status.Terminal() && p.handle.joinDone()
		return p.snapshot, nil
	}
	snapshot, err := processSnapshotFromWire(wire)
	if err == nil {
		p.snapshot = snapshot
		// Join proves that descendant work and child budget accounting have
		// drained; only a capture at that boundary can become permanent.
		p.snapshotDrained = p.status.Terminal() && p.handle.joinDone()
	}
	return snapshot, err
}

func (p *processState) result() Result {
	return Result{
		processID: p.handle.processID, startedAt: p.startedAt,
		finishedAt: p.finishedAt, output: p.finalOutput,
		termination: p.termination, usage: p.usage,
	}
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
	p.preparedExecution = candidate
	p.prepared = &prepared
	return nil
}

func (p *processState) stepSchedulingFailure() *stepPreparationFailure {
	reservedBudget := p.effectiveReservedBudget()
	if resourceQuantitiesFit(p.limits.MaxSteps, p.committedSteps, 1) &&
		resourceQuantitiesFit(p.budget.Steps, p.committedSteps, reservedBudget.Steps, 1) {
		return nil
	}
	return &stepPreparationFailure{
		kind: FailureKindExecution, code: "engine.limit.steps", cause: ErrResourceLimitExceeded,
	}
}

func (p *processState) prepareStepResult(result stepJobResult) *stepPreparationFailure {
	transition := result.transition
	if !transition.Valid() || uint64(transition.ConsumedSignals()) > result.deliveredSignals {
		return &stepPreparationFailure{
			kind: FailureKindContract, code: "execution.transition.invalid", cause: ErrInvalidTransition,
		}
	}
	effects := transition.Effects()
	for _, effect := range effects {
		if err := p.deployment.validateEffect(effect); err != nil {
			return &stepPreparationFailure{
				kind: FailureKindContract, code: "execution.effect.invalid", cause: err,
			}
		}
		if !p.capabilities.Allows(effect.RequiredCapabilities()) {
			return &stepPreparationFailure{
				kind: FailureKindContract, code: "engine.capability.denied", cause: ErrInvalidCapability,
			}
		}
	}
	effectCount := uint64(len(effects))
	reservedBudget := p.effectiveReservedBudget()
	if !resourceQuantitiesFit(p.limits.MaxEffects, p.usage.PreparedEffects, effectCount) ||
		!resourceQuantitiesFit(
			p.budget.Effects, p.usage.PreparedEffects, reservedBudget.Effects, effectCount,
		) {
		return &stepPreparationFailure{
			kind: FailureKindExecution, code: "engine.limit.effects", cause: ErrResourceLimitExceeded,
		}
	}
	remainingPending := p.mailbox.pendingCount() - uint64(transition.ConsumedSignals())
	if !resourceQuantitiesFit(p.limits.MaxSignals, p.usage.AcceptedSignals, effectCount) ||
		!resourceQuantitiesFit(p.limits.MaxPendingSignals, remainingPending, effectCount) ||
		!resourceQuantitiesFit(
			p.budget.Signals, p.usage.AcceptedSignals, reservedBudget.Signals, effectCount,
		) {
		return &stepPreparationFailure{
			kind: FailureKindExecution, code: "engine.limit.signals", cause: ErrResourceLimitExceeded,
		}
	}
	if output, completes := transition.Output(); completes {
		if validateOutputErr := p.deployment.Descriptor().ValidateOutput(output); validateOutputErr != nil {
			return &stepPreparationFailure{
				kind: FailureKindContract, code: "execution.output.invalid", cause: validateOutputErr,
			}
		}
	}
	digest, err := executionStateDigest(p.committedExecutionState)
	if err != nil {
		return &stepPreparationFailure{
			kind: FailureKindContract, code: "engine.committed_execution_state.invalid", cause: err,
		}
	}
	sequence := p.committedSteps + 1
	prepared := preparedStep{
		StepSequence: sequence, CommittedExecutionStateDigest: digest, CandidateState: result.candidateState,
		SignalCursor: p.mailbox.committedSignalCursor() + uint64(transition.ConsumedSignals()),
		Transition:   transition,
	}
	for index, effect := range effects {
		prepared.Effects = append(prepared.Effects, preparedEffect{
			ID: deriveEffectID(p.handle.processID, sequence, index), Effect: effect,
			Phase: effectPhasePlanned,
		})
	}
	p.prepared = &prepared
	p.preparedExecution = result.candidate
	p.usage.PreparedEffects += effectCount
	return nil
}

// Asynchronous failures wait for accepted external effects to settle before
// becoming terminal, just like cancellation and deadline intents.
func (p *processState) recordFailure(kind FailureKind, code string, err error) {
	if p.pendingControl.failure.Valid() {
		return
	}
	p.pendingControl.failure = newEngineFailure(kind, code, err)
}

func (p *processState) installTermination(termination Termination, output Output, finishedAt time.Time) {
	p.termination = termination
	p.status = termination.Status()
	p.finishedAt = finishedAt
	p.currentWaitID = WaitID{}
	p.pauseReason = ""
	p.pendingControl = pendingControl{}
	p.finalOutput = Output{}
	if p.status == StatusCompleted {
		p.finalOutput = output
	}
}

func (p *processState) resolveStepTermination(outcome stepOutcome) Termination {
	if p.pendingControl.failure.Valid() {
		outcome, _ = failedOutcome(p.pendingControl.failure)
	}
	termination, err := resolveTermination(terminationFacts{
		kill: p.pendingControl.kill, deadline: p.pendingControl.deadline,
		cancellation: p.pendingControl.cancellation, outcome: outcome,
	})
	if err != nil {
		failure, _ := NewFailure(FailureKindContract, "engine.termination.invalid", err.Error())
		termination = terminationForFailure(failure)
	}
	return termination
}

func (p *processState) effectiveTermination() Termination {
	if p.status.Terminal() {
		return p.termination
	}
	return p.resolveStepTermination(stepOutcome{})
}

func (p pendingControl) hasTerminalIntent() bool {
	return p.failure.Valid() || p.kill.valid() || p.deadline.valid() || p.cancellation.valid()
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
