package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"
)

type processState struct {
	// These references establish ownership and remain fixed while the runtime
	// owner goroutine mutates the execution fields below.
	handle     *processHandle
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
	finalOutput             Payload
	termination             Termination
	snapshot                ProcessSnapshot
	snapshotSealed          bool

	// Allocation and authority stay adjacent because every child reservation
	// must update both before it can become observable.
	limits             Limits
	treeLimits         TreeLimits
	allocatedResources resourceAmounts
	// Pending child-start Effects re-establish this reservation on restore.
	provisionalChildBudget *Budget
	capabilities           CapabilitySet
	counters               processCounters

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
	handle *processHandle,
	deployment Deployment,
	execution Execution,
	state ExecutionState,
	startedAt time.Time,
	limits Limits,
) *processState {
	return &processState{
		handle: handle, deployment: deployment, execution: execution,
		startedAt: startedAt, status: StatusRunning, committedExecutionState: state,
		mailbox: newSignalMailbox(), treeLimits: handle.treeLimits,
		capabilities: handle.capabilities, limits: limits,
	}
}

// Candidates own mutable protocol containers. Execution instances remain borrowed:
// only isolated worker jobs may call them, never a candidate transition.
func (p *processState) candidate() *processState {
	candidate := *p
	candidate.mailbox = p.mailbox.clone()
	if p.prepared != nil {
		prepared := p.prepared.clone()
		candidate.prepared = &prepared
	}
	return &candidate
}

func (p *processState) adoptCandidate(candidate *processState) {
	if candidate.handle != p.handle {
		panic("agent: candidate belongs to another Process")
	}
	*p = *candidate
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
func (p *processState) prepareSignals(signals []Signal, source signalSource) (*processState, error) {
	records, err := p.mailbox.prepareAdmission(p.status, p.currentWaitID, signals, source)
	if err != nil {
		return nil, err
	}
	if source == signalSourceExternal {
		for _, signal := range signals {
			if _, addressed := signal.WaitID(); addressed {
				continue
			}
			if err := p.deployment.Descriptor().ValidateSignal(Payload{data: signal.Payload()}); err != nil {
				return nil, err
			}
		}
	}
	if len(records) == 0 {
		return nil, nil
	}
	count := uint64(len(records))
	reserved := p.prepared.settlementSignalCount()
	remainingPending := p.mailbox.pendingCount()
	// The prepared cursor is bounded by accepted Signals and starts at the
	// committed mailbox cursor, so its consumption cannot exceed pending.
	remainingPending -= p.prepared.consumedSignals()
	allocated := p.effectiveAllocations()
	if !resourceQuantitiesFit(p.limits.MaxPendingSignals, p.mailbox.pendingCount(), count) ||
		!resourceQuantitiesFit(p.limits.MaxPendingSignals, remainingPending, reserved, count) ||
		!p.limits.Budget.Signals.Allows(p.usage().AcceptedSignals, allocated.Signals, reserved, count) {
		return nil, ErrResourceLimitExceeded
	}
	candidate := p.candidate()
	for _, record := range records {
		candidate.mailbox.acceptRecord(record)
	}
	if candidate.currentWaitID.Valid() && candidate.mailbox.waits[candidate.currentWaitID].answered {
		if candidate.status == StatusWaiting {
			candidate.status = StatusRunning
		}
		candidate.currentWaitID = WaitID{}
	}
	if _, err := candidate.snapshotAdmissionSize(); err != nil {
		return nil, err
	}
	return candidate, nil
}

func (p *processState) requestPause(reason string) error {
	if err := validateTerminationReason(reason); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidProcessControl, err)
	}
	if p.status != StatusRunning && p.status != StatusWaiting {
		return fmt.Errorf("%w: Pause requires Running or Waiting status, got %s", ErrInvalidProcessControl, p.status)
	}
	if p.pendingControl.pauseReason == "" {
		p.pendingControl.pauseReason = reason
	}
	return nil
}

func (p *processState) applyPendingPause() bool {
	if p.pendingControl.pauseReason == "" || (p.status != StatusRunning && p.status != StatusWaiting) {
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
	if p.currentWaitID.Valid() {
		p.status = StatusWaiting
	}
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

func (p *processState) prepareResolution(settlement Settlement) (*processState, int, error) {
	if p.prepared == nil {
		return nil, 0, ErrEffectNotPending
	}
	candidate := p.candidate()
	index, _, err := candidate.prepared.resolveUnknown(settlement)
	if err != nil {
		return nil, 0, err
	}
	if _, err := candidate.snapshotAdmissionSize(); err != nil {
		return nil, 0, err
	}
	return candidate, index, nil
}

func (p *processState) unknownEffectIDs() []EffectID {
	if p.prepared == nil {
		return nil
	}
	return p.prepared.Effects.unknownEffectIDs()
}

func (p *processState) reserveProvisionalChildBudget(requested Budget) bool {
	if p.provisionalChildBudget != nil {
		return false
	}
	reserved, ok := p.allocatedResources.add(resourceAmounts{Steps: 1, Signals: p.prepared.settlementSignalCount()})
	if !ok || !p.limits.Budget.canAllocate(p.usage(), reserved, requested) {
		return false
	}
	p.provisionalChildBudget = new(requested)
	return true
}

func (p *processState) commitProvisionalChildBudget(requested Budget) error {
	if p.provisionalChildBudget == nil || *p.provisionalChildBudget != requested {
		return ErrResourceLimitExceeded
	}
	debit, ok := p.limits.Budget.allocation(requested)
	if !ok {
		return ErrResourceLimitExceeded
	}
	allocated, ok := p.allocatedResources.add(debit)
	if !ok {
		return ErrResourceLimitExceeded
	}
	p.allocatedResources = allocated
	p.provisionalChildBudget = nil
	return nil
}

func (p *processState) releaseProvisionalChildBudget(requested Budget) {
	if p.provisionalChildBudget == nil || *p.provisionalChildBudget != requested {
		panic("agent: provisional child budget does not match its reservation")
	}
	p.provisionalChildBudget = nil
}

func (p *processState) releaseCommittedChildBudget(released Budget) {
	debit, ok := p.limits.Budget.allocation(released)
	if !ok || debit.Steps > p.allocatedResources.Steps || debit.Effects > p.allocatedResources.Effects || debit.Signals > p.allocatedResources.Signals {
		panic("agent: committed child budget exceeds its reservation")
	}
	p.allocatedResources.Steps -= debit.Steps
	p.allocatedResources.Effects -= debit.Effects
	p.allocatedResources.Signals -= debit.Signals
}

func (p *processState) effectiveAllocations() resourceAmounts {
	if p.provisionalChildBudget == nil {
		return p.allocatedResources
	}
	debit, ok := p.limits.Budget.allocation(*p.provisionalChildBudget)
	if !ok {
		panic("agent: invalid provisional child allocation")
	}
	reserved, ok := p.allocatedResources.add(debit)
	if !ok {
		panic("agent: Process resource reservation overflow")
	}
	return reserved
}

func (p *processState) capture() (ProcessSnapshot, error) {
	if p.snapshotSealed {
		return p.snapshot, nil
	}
	wire := p.snapshotWire()
	// Compare every persisted fact, including mailbox and Effect contents. This
	// avoids a second mutation protocol whose invalidation could miss a control,
	// reservation, or settlement while reusing the already validated value.
	if !p.snapshot.Valid() || !reflect.DeepEqual(wire, p.snapshot.state) {
		snapshot, err := processSnapshotFromWire(wire)
		if err != nil {
			return ProcessSnapshot{}, err
		}
		p.snapshot = snapshot
	}
	// Join proves descendant work and child accounting have drained; only a
	// capture at that boundary can become permanent.
	p.snapshotSealed = p.status.Terminal() && p.handle.joinDone()
	return p.snapshot, nil
}

func (p *processState) result() Result {
	return Result{
		processID: p.handle.processID, startedAt: p.startedAt,
		finishedAt: p.finishedAt, output: p.finalOutput,
		termination: p.termination, usage: p.usage(),
	}
}

func (p *processState) restorePreparedStep(ctx context.Context, stored *preparedStep, durable bool) error {
	if stored == nil {
		return nil
	}
	prepared := stored.clone()
	if err := p.validatePreparedWaits(&prepared); err != nil {
		return fmt.Errorf("%w: prepared waits: %w", ErrInvalidSnapshot, err)
	}
	if output, completes := prepared.Intent.Output(); completes {
		if err := p.deployment.Descriptor().ValidateOutput(output); err != nil {
			return fmt.Errorf("%w: prepared output schema: %w", ErrInvalidSnapshot, err)
		}
	}
	var candidate Execution
	if !p.status.Terminal() {
		var err error
		candidate, err = restoreExecution(ctx, p.deployment.Definition(), prepared.CandidateState)
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
			policy, err = dispatcherReplayPolicy(p.deployment.dispatcher, record.Effect)
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
	reserved := p.effectiveAllocations()
	if !p.limits.Budget.Steps.Allows(p.committedSteps, reserved.Steps, 1) {
		return &stepPreparationFailure{
			kind: FailureKindExecution, code: failureCodeEngineLimitSteps, cause: ErrResourceLimitExceeded,
		}
	}
	if p.committedSteps == ^uint64(0) {
		return &stepPreparationFailure{kind: FailureKindExecution, code: failureCodeEngineCounterExhausted, cause: ErrCounterExhausted}
	}
	return nil
}

func (p *processState) prepareStep(result stepJobResult) (*processState, *stepPreparationFailure) {
	transition := result.transition
	if !transition.Valid() || uint64(transition.ConsumedSignals()) > result.deliveredSignals {
		return nil, &stepPreparationFailure{
			kind: FailureKindContract, code: failureCodeExecutionTransitionInvalid, cause: ErrInvalidTransition,
		}
	}
	effects := transition.Effects()
	for _, effect := range effects {
		if err := p.deployment.validateEffect(effect); err != nil {
			return nil, &stepPreparationFailure{
				kind: FailureKindContract, code: failureCodeExecutionEffectInvalid, cause: err,
			}
		}
		if !p.capabilities.Allows(effect.RequiredCapabilities()) {
			return nil, &stepPreparationFailure{
				kind: FailureKindContract, code: failureCodeEngineCapabilityDenied, cause: ErrInvalidCapability,
			}
		}
	}
	effectCount := uint64(len(effects))
	allocated := p.effectiveAllocations()
	if !p.limits.Budget.Effects.Allows(p.counters.PreparedEffects, allocated.Effects, effectCount) {
		return nil, &stepPreparationFailure{
			kind: FailureKindExecution, code: failureCodeEngineLimitEffects, cause: ErrResourceLimitExceeded,
		}
	}
	if !resourceQuantitiesFit(^uint64(0), p.counters.PreparedEffects, effectCount) {
		return nil, &stepPreparationFailure{kind: FailureKindExecution, code: failureCodeEngineCounterExhausted, cause: ErrCounterExhausted}
	}
	remainingPending := p.mailbox.pendingCount() - uint64(transition.ConsumedSignals())
	if !resourceQuantitiesFit(p.limits.MaxPendingSignals, remainingPending, effectCount) ||
		!p.limits.Budget.Signals.Allows(p.usage().AcceptedSignals, allocated.Signals, effectCount) {
		return nil, &stepPreparationFailure{
			kind: FailureKindExecution, code: failureCodeEngineLimitSignals, cause: ErrResourceLimitExceeded,
		}
	}
	if output, completes := transition.Output(); completes {
		if validateOutputErr := p.deployment.Descriptor().ValidateOutput(output); validateOutputErr != nil {
			return nil, &stepPreparationFailure{
				kind: FailureKindContract, code: failureCodeExecutionOutputInvalid, cause: validateOutputErr,
			}
		}
	}
	digest, err := p.committedExecutionState.digest()
	if err != nil {
		return nil, &stepPreparationFailure{
			kind: FailureKindContract, code: failureCodeEngineCommittedExecutionStateInvalid, cause: err,
		}
	}
	sequence := p.committedSteps + 1
	transition.effects = nil
	prepared := preparedStep{
		StepSequence: sequence, CommittedExecutionStateDigest: digest, CandidateState: result.candidateState,
		SignalCursor: p.mailbox.committedSignalCursor() + uint64(transition.ConsumedSignals()),
		Intent:       transition,
	}
	for index, effect := range effects {
		prepared.Effects = append(prepared.Effects, preparedEffect{
			ID: p.handle.processID.effectID(sequence, index), Effect: effect,
			Phase: effectPhasePlanned,
		})
	}
	if err := p.validatePreparedWaits(&prepared); err != nil {
		return nil, &stepPreparationFailure{kind: FailureKindContract, code: failureCodeExecutionEffectInvalid, cause: err}
	}
	candidate := p.candidate()
	candidate.prepared = &prepared
	candidate.preparedExecution = result.candidate
	candidate.counters.PreparedEffects += effectCount
	if _, err := candidate.snapshotAdmissionSize(); err != nil {
		return nil, &stepPreparationFailure{kind: FailureKindExecution, code: failureCodeEngineLimitSnapshot, cause: err}
	}
	return candidate, nil
}

func (p *processState) snapshotAdmissionSize() (uint64, error) {
	return p.snapshotWire().admissionSize()
}

// Asynchronous failures wait for accepted external effects to settle before
// becoming terminal, just like cancellation and deadline intents.
func (p *processState) recordFailure(kind FailureKind, code string, err error) {
	if p.status.Terminal() || p.pendingControl.failure.Valid() {
		return
	}
	p.pendingControl.failure = newEngineFailure(kind, code, err)
}

func (p *processState) installTermination(termination Termination, output Payload, finishedAt time.Time) {
	if p.status.Terminal() {
		return
	}
	p.termination = termination
	p.status = termination.Status()
	p.finishedAt = finishedAt
	p.currentWaitID = WaitID{}
	p.pauseReason = ""
	p.pendingControl = pendingControl{}
	p.finalOutput = Payload{}
	if p.status == StatusCompleted {
		p.finalOutput = output
	}
}

func (p *processState) resolveStepTermination(outcome stepOutcome) Termination {
	if p.pendingControl.failure.Valid() {
		outcome, _ = failedOutcome(p.pendingControl.failure)
	}
	termination, err := (terminationFacts{
		kill: p.pendingControl.kill, deadline: p.pendingControl.deadline,
		cancellation: p.pendingControl.cancellation, outcome: outcome,
	}).resolve()
	if err != nil {
		failure := newEngineFailure(FailureKindContract, failureCodeEngineTerminationInvalid, err)
		termination = failure.termination()
	}
	return termination
}

func (p *processState) effectiveTermination() Termination {
	if p.status.Terminal() {
		return p.termination
	}
	return p.resolveStepTermination(stepOutcome{})
}

func (p *processState) terminalEventPayload() json.RawMessage {
	usage := p.usage()
	eventPayload := processFinishedEventPayload{
		ProcessStatus:    p.status,
		TerminationCause: p.termination.Cause(),
		Usage:            &usage,
	}
	if failure, failed := p.termination.Failure(); failed {
		eventPayload.FailureKind = failure.Kind()
		eventPayload.FailureCode = failure.Code()
	}
	return marshalEventPayload(eventPayload)
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

// Pure wait conflicts must be rejected before any dispatcher receives permission.
func (p *processState) validatePreparedWaits(prepared *preparedStep) error {
	finalization, err := newPreparedStepFinalization(p, prepared)
	if err != nil {
		return err
	}
	for _, record := range prepared.Effects {
		if record.Effect.Target() != EffectTargetFramework {
			continue
		}
		operation, err := decodeFrameworkEffectOperation(record.Effect.Payload())
		if err != nil {
			return err
		}
		if operation != frameworkEffectWait && operation != frameworkEffectWaitChildren {
			continue
		}
		record = preparedEffect{ID: record.ID, Effect: record.Effect, Phase: effectPhasePlanned}
		if err := record.begin(); err != nil {
			return err
		}
		if err := record.settleFramework(); err != nil {
			return err
		}
		if err := finalization.applySettlement(record); err != nil {
			return err
		}
	}
	return nil
}

func (p *processState) usage() Usage {
	return Usage{CommittedSteps: p.committedSteps, AcceptedSignals: p.mailbox.acceptedCount(), PreparedEffects: p.counters.PreparedEffects, DroppedDeltas: p.counters.DroppedDeltas}
}

func (p *processState) adopt(finalization *preparedStepFinalization) {
	p.execution = p.preparedExecution
	p.preparedExecution = nil
	p.committedExecutionState = finalization.prepared.CandidateState
	p.mailbox = finalization.mailbox
	p.committedSteps = finalization.prepared.StepSequence
	p.prepared = nil
	if finalization.commit.termination.Valid() {
		p.installTermination(finalization.commit.termination, finalization.commit.finalOutput, finalization.commit.finishedAt)
	} else {
		p.status = finalization.commit.status
		p.currentWaitID = finalization.commit.currentWaitID
		p.pauseReason = finalization.commit.pauseReason
	}
}

func (p *processState) snapshotWire() processSnapshotWire {
	wire := processSnapshotWire{
		ProcessID:     p.handle.processID,
		Relation:      p.handle.relation.wire(),
		DeploymentRef: p.deployment.DeploymentRef(), StartedAt: p.startedAt,
		Status: p.status, CommittedSteps: p.committedSteps,
		TreeLimits:         p.treeLimits,
		AllocatedResources: p.allocatedResources,
		Capabilities:       p.capabilities, Counters: p.counters,
		CommittedExecutionState: p.committedExecutionState, Mailbox: p.mailbox.wire(),
		PauseReason: p.pauseReason, PendingControl: p.pendingControl.wire(), Limits: p.limits,
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
		prepared := p.prepared.clone()
		wire.Prepared = &prepared
	}
	return wire
}
