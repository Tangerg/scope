package agent

import (
	"context"
	"errors"
	"fmt"
	"time"
)

type processState struct {
	// These references establish ownership and remain fixed while the runtime
	// owner goroutine mutates the execution fields below.
	handle     *processHandleState
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
	p.mailbox = candidate
	if p.status == StatusWaiting && status == StatusRunning {
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

func (p pendingControl) hasTerminalIntent() bool {
	return p.failure.Valid() || p.kill.valid() || p.deadline.valid() || p.cancellation.valid()
}
