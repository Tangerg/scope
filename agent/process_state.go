package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"reflect"
	"slices"
	"time"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/agent/internal/jsonwire"
)

type processState struct {
	// These references establish ownership and remain fixed while the runtime
	// owner goroutine mutates the execution fields below.
	handle *processHandle

	// Only treeRuntime's owner goroutine mutates protocol and recovery state, keeping
	// snapshots and externally visible transitions in one deterministic order.
	execution               Execution
	preparedExecution       Execution
	committedSteps          uint64
	processEventSequence    uint64
	committedExecutionState ExecutionState
	mailbox                 signalMailbox
	prepared                *preparedStep
	currentWaitID           WaitID
	pause                   pause
	pendingControl          pendingControl
	finish                  *processFinish
	snapshot                ProcessSnapshot
	snapshotSealed          bool

	counters processCounters

	// Restore bookkeeping is consumed by the owner goroutine before admitting
	// new work, preventing recovered effects from racing fresh execution.
	restored bool
	// recoveryOwed marks the one pending Effect a restored prepared Step
	// retains until its recovery decision runs; the Dispatcher's Policy decides
	// that recovery when it runs.
	recoveryOwed    bool
	attemptSequence uint64
}

type pendingControl struct {
	failure      Failure
	kill         killIntent
	deadline     deadlineIntent
	cancellation cancellationIntent
	pause        pause
}

func newProcessState(
	handle *processHandle,
	execution Execution,
	state ExecutionState,
) *processState {
	return &processState{
		handle: handle, execution: execution,
		committedExecutionState: state,
		mailbox:                 newSignalMailbox(),
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

// preparedStepSequence numbers the Step that would follow committed progress.
func (p *processState) preparedStepSequence() uint64 { return p.committedSteps + 1 }

// deployment is the exact binding the Process's handle owns.
func (p *processState) deployment() Deployment { return p.handle.deployment }

func (p *processState) status() Status {
	return lifecycleStatus(lo.FromPtr(p.finish).Termination, p.pause.valid(), p.awaitedWaitID().Valid())
}

func (p *processState) awaitedWaitID() WaitID { return p.mailbox.awaited(p.currentWaitID) }

func (p *processState) adoptCandidate(candidate *processState) {
	if candidate.handle != p.handle {
		panic("agent: candidate belongs to another Process")
	}
	*p = *candidate
}

func (p *processState) recordHostTermination(err error) {
	if errors.Is(err, context.DeadlineExceeded) {
		intent, _ := newDeadlineIntent(deadlineOwnerHost, "host context deadline reached")
		p.pendingControl.recordDeadline(intent)
		return
	}
	intent, _ := newCancellationIntent(cancellationOwnerHost, "host context canceled")
	p.pendingControl.recordCancellation(intent)
}

func (p *processState) recordParentTermination(parent Termination) {
	if !parent.Valid() {
		return
	}
	if parent.Status() == StatusTimedOut {
		intent, _ := newDeadlineIntent(deadlineOwnerParent, "parent Process reached a deadline")
		p.pendingControl.recordDeadline(intent)
		return
	}
	intent, _ := newCancellationIntent(
		cancellationOwnerParent,
		"parent Process reached terminal status "+parent.Status().String(),
	)
	p.pendingControl.recordCancellation(intent)
}

// Admission validates the complete batch before changing mailbox or wait state.
func (p *processState) prepareSignals(signals []Signal, source signalSource, limits TreeLimits, childAllocation resourceAmounts) (*processState, error) {
	records, err := p.mailbox.prepareAdmission(p.status(), p.awaitedWaitID(), signals, source)
	if err != nil {
		return nil, err
	}
	if source == signalSourceExternal {
		for _, signal := range signals {
			if _, addressed := signal.WaitID(); addressed {
				continue
			}
			if err := p.deployment().Descriptor().ValidateSignal(Payload{data: signal.Payload()}); err != nil {
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
	// Step preparation bounds consumption by the pending suffix. Admission must
	// also fit the settlements appended when that candidate is adopted.
	remainingPending -= p.prepared.consumedSignals()
	allocated := childAllocation
	if !limits.admitsPendingSignals(p.mailbox.pendingCount(), remainingPending, reserved, count) ||
		!p.handle.budget.Signals.Allows(p.usage().AcceptedSignals, allocated.Signals, reserved, count) {
		return nil, ErrResourceLimitExceeded
	}
	candidate := p.candidate()
	for _, record := range records {
		candidate.mailbox.acceptRecord(record)
	}
	if _, err := candidate.snapshotAdmissionSize(limits); err != nil {
		return nil, err
	}
	return candidate, nil
}

func (p *processState) requestPause(reason string) error {
	requested, err := newPause(reason)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidProcessControl, err)
	}
	if !p.status().pausable() {
		return fmt.Errorf("%w: Pause requires Running or Waiting status, got %s", ErrInvalidProcessControl, p.status())
	}
	p.pendingControl.recordPause(requested)
	return nil
}

func (p *processState) applyPendingPause() bool {
	if !p.pendingControl.pause.valid() || !p.status().pausable() {
		return false
	}
	p.pause = p.pendingControl.pause
	p.pendingControl.pause = pause{}
	return true
}

func (p *processState) resume() error {
	if p.status() != StatusPaused {
		return fmt.Errorf("%w: Resume requires Paused status, got %s", ErrInvalidProcessControl, p.status())
	}
	p.pause = pause{}
	p.pendingControl.pause = pause{}
	return nil
}

func (p *processState) requestCancellation(intent cancellationIntent) {
	p.pendingControl.recordCancellation(intent)
}

func (p *processState) requestKill(reason string) error {
	intent, err := newKillIntent(reason)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidProcessControl, err)
	}
	p.pendingControl.recordKill(intent)
	return nil
}

func (p *processState) prepareResolution(effectID EffectID, settlement Settlement, limits TreeLimits) (*processState, int, error) {
	if p.prepared == nil {
		return nil, 0, ErrEffectNotPending
	}
	candidate := p.candidate()
	index, _, err := candidate.prepared.resolveUnknown(effectID, settlement)
	if err != nil {
		return nil, 0, err
	}
	if _, err := candidate.snapshotAdmissionSize(limits); err != nil {
		return nil, 0, err
	}
	return candidate, index, nil
}

// publishedTermination is the Termination callers observe. The prepared
// Effects left unknown own the identities it leaves unresolved, so the
// installed termination never stores a copy.
func (p *processState) publishedTermination() Termination {
	if p.finish == nil {
		return Termination{}
	}
	return p.finish.Termination.withUnresolvedEffectIDs(p.unknownEffectIDs())
}

func (p *processState) unknownEffectIDs() []EffectID {
	if p.prepared == nil {
		return nil
	}
	return p.prepared.Effects.unknownEffectIDs()
}

// canReserveChildBudget reports whether requested fits beside the child
// debits already held against this Process's budget.
func (p *processState) canReserveChildBudget(requested Budget, childAllocation resourceAmounts) bool {
	reserved, ok := childAllocation.add(resourceAmounts{Steps: 1, Signals: p.prepared.settlementSignalCount()})
	return ok && p.handle.budget.canAllocate(p.usage(), reserved, requested)
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
	p.snapshotSealed = p.status().Terminal() && p.handle.joinDone()
	return p.snapshot, nil
}

func (p *processState) result() Result {
	return Result{
		processID: p.handle.processID(), startedAt: p.handle.startedAt,
		finishedAt: lo.FromPtr(p.finish).FinishedAt, output: lo.FromPtr(p.finish).Output,
		termination: p.publishedTermination(), usage: p.usage(),
	}
}

func (p *processState) restorePreparedStep(ctx context.Context, stored *preparedStep) error {
	if stored == nil {
		return nil
	}
	prepared := stored.clone()
	if err := p.validatePreparedWaits(&prepared); err != nil {
		return fmt.Errorf("%w: prepared waits: %w", ErrInvalidSnapshot, err)
	}
	if output, completes := prepared.Intent.Output(); completes {
		if err := p.deployment().Descriptor().ValidateOutput(output); err != nil {
			return fmt.Errorf("%w: prepared output schema: %w", ErrInvalidSnapshot, err)
		}
	}
	var candidate Execution
	if !p.status().Terminal() {
		var err error
		candidate, err = restoreExecution(ctx, p.deployment().Definition(), prepared.CandidateState)
		if err != nil {
			return fmt.Errorf("%w: restore prepared Execution: %w", ErrInvalidSnapshot, err)
		}
	}
	for index := range prepared.Effects {
		record := &prepared.Effects[index]
		if err := p.deployment().validateEffect(record.Effect); err != nil {
			return fmt.Errorf("%w: prepared Effect: %w", ErrInvalidSnapshot, err)
		}
		// Only a Process that can still dispatch asks its Dispatcher; a terminal
		// or terminating Process retains its batch as evidence.
		var effectPolicy EffectPolicy
		if record.Effect.Target() == EffectTargetDispatcher && !p.status().Terminal() &&
			!p.pendingControl.hasTerminalIntent() && record.phase() != effectPhaseSettled {
			var err error
			if effectPolicy, err = dispatcherEffectPolicy(p.deployment().dispatcher, record.Effect); err != nil {
				return fmt.Errorf("%w: restore prepared Effect: %w", ErrInvalidSnapshot, err)
			}
			if !p.handle.capabilities.Allows(effectPolicy.RequiredCapabilities) {
				return fmt.Errorf("%w: prepared Effect capability denied: %w", ErrInvalidSnapshot, ErrInvalidCapability)
			}
		}
		if record.phase() != effectPhasePending {
			continue
		}
		if record.Effect.Target() == EffectTargetFramework {
			operation, err := decodeFrameworkOperation(record.Effect.Payload())
			if err != nil {
				return fmt.Errorf("%w: restore pending framework Effect: %w", ErrInvalidSnapshot, err)
			}
			if _, starts := operation.(childStartOperation); !starts {
				continue
			}
		}
		if p.recoveryOwed {
			return fmt.Errorf("%w: multiple pending Effects", ErrInvalidSnapshot)
		}
		p.recoveryOwed = true
	}
	p.preparedExecution = candidate
	p.prepared = &prepared
	return nil
}

func (p *processState) stepSchedulingFailure(childAllocation resourceAmounts) *stepFailure {
	if !p.handle.budget.Steps.Allows(p.committedSteps, childAllocation.Steps, 1) {
		return &stepFailure{
			kind: FailureKindExecution, code: failureCodeEngineLimitSteps, cause: ErrResourceLimitExceeded,
		}
	}
	if p.committedSteps == math.MaxUint64 {
		return &stepFailure{kind: FailureKindExecution, code: failureCodeEngineCounterExhausted, cause: ErrCounterExhausted}
	}
	return nil
}

func (p *processState) prepareStep(result stepJobResult, limits TreeLimits, childAllocation resourceAmounts) (*processState, *stepFailure) {
	transition := result.transition
	if !transition.Valid() || uint64(transition.ConsumedSignals()) > result.deliveredSignals {
		return nil, &stepFailure{
			kind: FailureKindContract, code: failureCodeExecutionTransitionInvalid, cause: ErrInvalidTransition,
		}
	}
	effects := transition.Effects()
	for _, effect := range effects {
		if err := p.deployment().validateEffect(effect); err != nil {
			return nil, &stepFailure{
				kind: FailureKindContract, code: failureCodeExecutionEffectInvalid, cause: err,
			}
		}
		if effect.Target() != EffectTargetDispatcher {
			continue
		}
		policy, err := dispatcherEffectPolicy(p.deployment().dispatcher, effect)
		if err != nil {
			return nil, &stepFailure{kind: FailureKindContract, code: failureCodeExecutionEffectInvalid, cause: err}
		}
		if !p.handle.capabilities.Allows(policy.RequiredCapabilities) {
			return nil, &stepFailure{
				kind: FailureKindContract, code: failureCodeEngineCapabilityDenied, cause: ErrInvalidCapability,
			}
		}
	}
	effectCount := uint64(len(effects))
	if !p.handle.budget.Effects.Allows(p.counters.PreparedEffects, childAllocation.Effects, effectCount) {
		return nil, &stepFailure{
			kind: FailureKindExecution, code: failureCodeEngineLimitEffects, cause: ErrResourceLimitExceeded,
		}
	}
	if !resourceQuantitiesFit(math.MaxUint64, p.counters.PreparedEffects, effectCount) {
		return nil, &stepFailure{kind: FailureKindExecution, code: failureCodeEngineCounterExhausted, cause: ErrCounterExhausted}
	}
	remainingPending := p.mailbox.pendingCount() - uint64(transition.ConsumedSignals())
	if !limits.admitsPendingSignals(p.mailbox.pendingCount(), remainingPending, effectCount, 0) ||
		!p.handle.budget.Signals.Allows(p.usage().AcceptedSignals, childAllocation.Signals, effectCount) {
		return nil, &stepFailure{
			kind: FailureKindExecution, code: failureCodeEngineLimitSignals, cause: ErrResourceLimitExceeded,
		}
	}
	if output, completes := transition.Output(); completes {
		if validateOutputErr := p.deployment().Descriptor().ValidateOutput(output); validateOutputErr != nil {
			return nil, &stepFailure{
				kind: FailureKindContract, code: failureCodeExecutionOutputInvalid, cause: validateOutputErr,
			}
		}
	}
	sequence := p.preparedStepSequence()
	transition.effects = nil
	prepared := preparedStep{CandidateState: result.candidateState, Intent: transition}
	for index, effect := range effects {
		prepared.Effects = append(prepared.Effects, preparedEffect{
			ID: p.handle.processID().effectID(sequence, index), Effect: effect,
		})
	}
	if err := p.validatePreparedWaits(&prepared); err != nil {
		return nil, &stepFailure{kind: FailureKindContract, code: failureCodeExecutionEffectInvalid, cause: err}
	}
	candidate := p.candidate()
	candidate.prepared = &prepared
	candidate.preparedExecution = result.candidate
	candidate.counters.PreparedEffects += effectCount
	if _, err := candidate.snapshotAdmissionSize(limits); err != nil {
		return nil, &stepFailure{kind: FailureKindExecution, code: failureCodeEngineLimitSnapshot, cause: err}
	}
	return candidate, nil
}

func (p *processState) snapshotAdmissionSize(limits TreeLimits) (uint64, error) {
	if !limits.MaxProcessSnapshotBytes.limited && !limits.MaxSnapshotBytes.limited {
		return 0, nil
	}
	return p.snapshotWire().admissionSize(limits)
}

// Asynchronous failures wait for accepted external effects to settle before
// becoming terminal, just like cancellation and deadline intents.
func (p *processState) recordFailure(kind FailureKind, code string, err error) {
	if p.status().Terminal() {
		return
	}
	p.pendingControl.recordFailure(newEngineFailure(kind, code, err))
}

// processFinish is a terminal Process's committed ending: a Process has
// finished exactly when it has one, and only a Completion carries Output.
type processFinish struct {
	Termination Termination `json:"termination"`
	FinishedAt  time.Time   `json:"finished_at"`
	Output      Payload     `json:"output,omitzero"`
}

func (p *processFinish) UnmarshalJSON(data []byte) error {
	type wire processFinish
	value, err := jsonwire.Decode[wire](data, "termination", "finished_at")
	if err != nil {
		return err
	}
	*p = processFinish(value)
	return nil
}

func (p *processState) installTermination(termination Termination, output Payload, finishedAt time.Time) {
	if p.status().Terminal() {
		return
	}
	p.finish = &processFinish{Termination: termination, FinishedAt: finishedAt}
	p.currentWaitID = WaitID{}
	p.pause = pause{}
	p.pendingControl = pendingControl{}
	if p.status() == StatusCompleted {
		p.finish.Output = output
	}
}

func (p *processState) resolveStepTermination(outcome stepOutcome) Termination {
	if p.pendingControl.failure.Valid() {
		outcome, _ = failedOutcome(p.pendingControl.failure)
	}
	termination, err := (terminationInputs{
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
	if p.finish != nil {
		return p.finish.Termination
	}
	return p.resolveStepTermination(stepOutcome{})
}

func (p *processState) terminalEventPayload() json.RawMessage {
	usage := p.usage()
	eventPayload := processFinishedEventPayload{
		Usage: &usage,
	}
	termination := lo.FromPtr(p.finish).Termination
	if failure, failed := termination.Failure(); failed {
		eventPayload.FailureKind = failure.Kind()
		eventPayload.FailureCode = failure.Code()
	} else {
		eventPayload.TerminationCause = termination.Cause()
	}
	return marshalEventPayload(eventPayload)
}

// The first recorded intent of each kind is authoritative; later sources of
// the same kind cannot rewrite its owner or reason.
func (p *pendingControl) recordFailure(failure Failure) {
	if !p.failure.Valid() {
		p.failure = failure
	}
}

func (p *pendingControl) recordKill(intent killIntent) {
	if !p.kill.valid() {
		p.kill = intent
	}
}

func (p *pendingControl) recordDeadline(intent deadlineIntent) {
	if !p.deadline.valid() {
		p.deadline = intent
	}
}

func (p *pendingControl) recordCancellation(intent cancellationIntent) {
	if !p.cancellation.valid() {
		p.cancellation = intent
	}
}

func (p *pendingControl) recordPause(requested pause) {
	if !p.pause.valid() {
		p.pause = requested
	}
}

func (p pendingControl) hasTerminalIntent() bool {
	return p.failure.Valid() || p.kill.valid() || p.deadline.valid() || p.cancellation.valid()
}

func (p pendingControl) wire() pendingControlWire {
	wire := pendingControlWire{PauseReason: p.pause.reason}
	if p.failure.Valid() {
		failure := p.failure
		wire.Failure = &failure
	}
	if p.kill.valid() {
		wire.KillReason = p.kill.reason
	}
	if p.deadline.valid() {
		wire.Deadline = &deadlineIntentWire{Owner: p.deadline.owner, Reason: p.deadline.reason}
	}
	if p.cancellation.valid() {
		wire.Cancellation = &cancellationIntentWire{Owner: p.cancellation.owner, Reason: p.cancellation.reason}
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
		operation, err := decodeFrameworkOperation(record.Effect.Payload())
		if err != nil {
			return err
		}
		switch operation.(type) {
		case waitOperation, childWaitOperation:
		default:
			continue
		}
		record = preparedEffect{ID: record.ID, Effect: record.Effect}
		if err := record.settleLocally(operation); err != nil {
			return err
		}
		if err := finalization.applySettlement(record); err != nil {
			return err
		}
	}
	return nil
}

func (p *processState) usage() Usage {
	return p.counters.usage(p.committedSteps, p.mailbox.acceptedCount())
}

// An unadopted candidate and the executable instance restored for it live
// and die together.
func (p *processState) discardPreparedStep() {
	p.prepared = nil
	p.preparedExecution = nil
}

func (p *processState) adopt(finalization *preparedStepFinalization) {
	p.execution = p.preparedExecution
	p.preparedExecution = nil
	p.committedExecutionState = finalization.prepared.CandidateState
	p.mailbox = finalization.mailbox
	p.committedSteps = p.preparedStepSequence()
	p.prepared = nil
	if commit := finalization.commit; commit.termination.Valid() {
		p.installTermination(commit.termination, finalization.finalOutput(), commit.finishedAt)
	} else {
		p.currentWaitID, _ = finalization.prepared.Intent.WaitID()
		p.pause = finalization.prepared.Intent.pause
	}
}

func (p *processState) snapshotWire() processSnapshotWire {
	wire := processSnapshotWire{
		Relation:      p.handle.relation,
		DeploymentRef: p.deployment().DeploymentRef(), StartedAt: p.handle.startedAt,
		CommittedSteps: p.committedSteps,
		Budget:         p.handle.budget, Capabilities: p.handle.capabilities, Counters: p.counters,
		CommittedExecutionState: p.committedExecutionState, Mailbox: p.mailbox.wire(),
		PauseReason: p.pause.reason, PendingControl: p.pendingControl.wire(),
	}
	if p.currentWaitID.Valid() {
		waitID := p.currentWaitID
		wire.CurrentWaitID = &waitID
	}
	if p.finish != nil {
		wire.Finish = new(*p.finish)
	}
	if p.prepared != nil {
		prepared := p.prepared.clone()
		wire.Prepared = &prepared
	}
	return wire
}

func orderedProcesses(values map[ProcessID]*processState) []*processState {
	processes := slices.Collect(maps.Values(values))
	slices.SortFunc(processes, func(left, right *processState) int {
		return left.handle.relation.compareTreeOrder(right.handle.relation)
	})
	return processes
}
