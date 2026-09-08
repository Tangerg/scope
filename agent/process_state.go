package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

type processState struct {
	// These references establish ownership and remain fixed while the runtime
	// owner goroutine mutates the execution fields below.
	engine     *Engine
	controller *processController
	deployment Deployment
	execution  Execution

	// Only treeRuntime's owner goroutine mutates protocol and recovery state, keeping
	// snapshots and externally visible transitions in one deterministic order.
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
	runtime         *treeRuntime
	attemptSequence uint64
}

type restoredPendingEffect struct {
	id           EffectID
	replayPolicy ReplayPolicy
}

func (r restoredPendingEffect) matches(effectID EffectID) bool {
	return r.id.Valid() && r.id == effectID && r.replayPolicy.Valid()
}

type preparedStep struct {
	wire      preparedStepWire
	candidate Execution
}

type pendingControl struct {
	failure      Failure
	kill         killIntent
	deadline     deadlineIntent
	cancellation cancellationIntent
	pauseReason  string
}

func newProcessState(
	engine *Engine,
	controller *processController,
	deployment Deployment,
	execution Execution,
	state ExecutionState,
	startedAt time.Time,
	limits Limits,
) *processState {
	return &processState{
		engine: engine, controller: controller, deployment: deployment, execution: execution,
		startedAt: startedAt, status: StatusRunning, committedExecutionState: state,
		mailbox: newSignalMailbox(), limits: limits, treeLimits: engine.treeLimits,
		budget: controller.budget, capabilities: controller.capabilities,
	}
}

func (p *processState) applyPendingControl(ctx context.Context) bool {
	if p.pendingControl.hasTerminalIntent() {
		p.commitTermination(stepOutcome{})
		return true
	}
	if p.pendingControl.pauseReason == "" || p.status != StatusRunning {
		return false
	}
	p.status = StatusPaused
	p.pauseReason = p.pendingControl.pauseReason
	p.pendingControl.pauseReason = ""
	p.updateView()
	p.publishEventAfterCheckpoint(
		ctx, EventProcessPaused, EventPhaseCommitted, 0, EffectID{}, emptyEventPayload(),
	)
	return true
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

func (p *processState) applyCommand(ctx context.Context, command processCommand) {
	if p.status.Terminal() {
		command.reply(processResponse{err: ErrProcessFinished})
		return
	}
	switch command.kind {
	case commandDeliverBatch:
		p.deliverBatch(ctx, command)
	case commandPause:
		p.requestPause(command)
	case commandResume:
		p.resume(ctx, command)
	case commandCancel:
		p.requestCancellation(command.cancellationIntent)
	case commandKill:
		p.requestKill(command)
	case commandResolveUnknownEffect:
		p.resolveEffect(command)
	default:
		command.reply(processResponse{err: ErrProcessNotRunning})
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

func (p *processState) deliverChildrenCompleted(ctx context.Context, signal Signal) bool {
	accepted, err := p.admitSignals([]Signal{signal}, signalSourceChildCompletion)
	if err != nil {
		if errors.Is(err, ErrResourceLimitExceeded) {
			p.recordFailure(FailureKindExecution, "engine.limit.child_completion_signal", err)
		} else {
			p.recordFailure(FailureKindContract, "engine.child.completion.invalid", err)
		}
		return false
	}
	if accepted {
		p.updateView()
		for _, event := range p.prepareSignalEvents([]Signal{signal}) {
			p.publishPreparedEvent(ctx, event)
		}
	}
	return accepted || p.mailbox.contains(signal.ID())
}

func (p *processState) deliverBatch(ctx context.Context, command processCommand) {
	if len(command.signalRequests) == 0 {
		command.reply(processResponse{err: ErrInvalidSignalRequest})
		return
	}
	signals := make([]Signal, 0, len(command.signalRequests))
	for _, request := range command.signalRequests {
		signal, err := request.signal()
		if err != nil {
			command.reply(processResponse{err: err})
			return
		}
		signals = append(signals, signal)
	}
	accepted, err := p.admitSignals(signals, signalSourceExternal)
	if err != nil || !accepted {
		command.reply(processResponse{err: err})
		return
	}
	events := p.prepareSignalEvents(signals)
	if p.engine.durability != nil {
		if err := p.runtime.startSignalCommit(p, command, events); err != nil {
			command.reply(processResponse{err: err})
			p.runtime.failDurability(err, p.controller.processID, EffectID{})
		}
		return
	}
	p.updateView()
	for _, event := range events {
		p.publishPreparedEvent(ctx, event)
	}
	command.reply(processResponse{accepted: true})
}

// Admission validates identity and wait authority before charging resources.
// The candidate keeps a rejected batch from changing any mailbox or wait state.
func (p *processState) admitSignals(signals []Signal, source signalSource) (bool, error) {
	candidate := p.mailbox.clone()
	status := p.status
	duplicate := false
	for _, signal := range signals {
		accepted, err := candidate.enqueue(status, signal, source)
		if err != nil {
			return false, err
		}
		if !accepted {
			duplicate = true
			continue
		}
		if status == StatusWaiting {
			waitID, _ := signal.WaitID()
			if source == signalSourceExternal {
				wait := p.mailbox.waits[p.currentWaitID]
				if waitID != p.currentWaitID && (waitID.Valid() || wait.externallyAddressable) {
					return false, ErrSignalRejected
				}
			}
			if waitID == p.currentWaitID {
				status = StatusRunning
			}
		}
	}
	if duplicate {
		return false, nil
	}
	count := uint64(len(signals))
	reserved := p.reservedSettlementSignals()
	remainingPending := p.mailbox.pendingCount()
	if p.prepared != nil {
		remainingPending -= uint64(p.prepared.wire.Transition.ConsumedSignals())
	}
	reservedBudget := p.effectiveReservedBudget()
	if !resourceQuantitiesFit(p.limits.MaxSignals, p.usage.AcceptedSignals, reserved, count) ||
		!resourceQuantitiesFit(p.limits.MaxPendingSignals, p.mailbox.pendingCount(), count) ||
		!resourceQuantitiesFit(p.limits.MaxPendingSignals, remainingPending, reserved, count) ||
		!resourceQuantitiesFit(p.budget.Signals, p.usage.AcceptedSignals, reservedBudget.Signals, reserved, count) {
		return false, ErrResourceLimitExceeded
	}
	p.mailbox = candidate
	if p.status == StatusWaiting && status == StatusRunning {
		p.status = StatusRunning
		p.currentWaitID = WaitID{}
	}
	p.usage.AcceptedSignals += count
	return true, nil
}

func (p *processState) prepareSignalEvents(signals []Signal) []Event {
	var events []Event
	for _, signal := range signals {
		waitID, _ := signal.WaitID()
		payload, _ := json.Marshal(signalAcceptedEventPayload{SignalID: signal.ID().String(), WaitID: waitID.String()})
		if event, ok := p.prepareEvent(EventSignalAccepted, EventPhaseCommitted, 0, EffectID{}, payload); ok {
			events = append(events, event)
		}
	}
	return events
}

func (p *processState) requestPause(command processCommand) {
	if err := validateTerminationReason(command.reason); err != nil {
		command.reply(processResponse{err: fmt.Errorf("%w: %w", ErrInvalidProcessControl, err)})
		return
	}
	if p.status != StatusRunning {
		command.reply(processResponse{err: ErrProcessNotRunning})
		return
	}
	if p.pendingControl.pauseReason == "" {
		p.pendingControl.pauseReason = command.reason
	}
	command.reply(processResponse{})
}

func (p *processState) resume(ctx context.Context, command processCommand) {
	if p.status != StatusPaused {
		command.reply(processResponse{err: ErrProcessNotRunning})
		return
	}
	p.status = StatusRunning
	p.pauseReason = ""
	p.pendingControl.pauseReason = ""
	p.updateView()
	p.publishEvent(ctx, EventProcessResumed, EventPhaseCommitted, 0, EffectID{}, emptyEventPayload())
	command.reply(processResponse{})
}

func (p *processState) requestCancellation(intent cancellationIntent) {
	if !p.pendingControl.cancellation.valid() {
		p.pendingControl.cancellation = intent
	}
}

func (p *processState) requestKill(command processCommand) {
	intent, err := newKillIntent(command.reason)
	if err != nil {
		command.reply(processResponse{err: fmt.Errorf("%w: %w", ErrInvalidProcessControl, err)})
		return
	}
	if !p.pendingControl.kill.valid() {
		p.pendingControl.kill = intent
	}
	command.reply(processResponse{})
}

func (p *processState) resolveEffect(command processCommand) {
	if p.prepared == nil || !command.settlement.Valid() || command.settlement.Status() == SettlementStatusUnknown {
		command.reply(processResponse{err: ErrEffectNotPending})
		return
	}
	for index := range p.prepared.wire.Effects {
		effect := &p.prepared.wire.Effects[index]
		if effect.ID != command.settlement.EffectID() {
			continue
		}
		if !effect.unknown() {
			command.reply(processResponse{err: ErrEffectNotPending})
			return
		}
		if err := effect.resolveUnknown(command.settlement); err != nil {
			command.reply(processResponse{err: ErrEffectNotPending})
			return
		}
		command.reply(processResponse{})
		return
	}
	command.reply(processResponse{err: ErrEffectNotPending})
}

func (p *processState) updateView() {
	if p.engine.durability == nil {
		p.controller.updateStatus(p.status)
	}
}

func (p *processState) unknownEffectIDs() []EffectID {
	if p.prepared == nil {
		return nil
	}
	return p.prepared.wire.Effects.unknownEffectIDs()
}

func (p *processState) reservedSettlementSignals() uint64 {
	if p.prepared == nil {
		return 0
	}
	return uint64(len(p.prepared.wire.Effects))
}

func (p pendingControl) hasTerminalIntent() bool {
	return p.failure.Valid() || p.kill.valid() || p.deadline.valid() || p.cancellation.valid()
}

func (p *preparedStep) hasUnknownSettlement() bool {
	for _, effect := range p.wire.Effects {
		if effect.unknown() {
			return true
		}
	}
	return false
}
