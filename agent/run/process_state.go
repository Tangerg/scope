package run

import (
	"fmt"
	"math"
	"slices"
	"strings"
)

type processState struct {
	done             chan struct{}
	queued           bool
	started          Started
	step             uint64
	inbox            Inbox
	receipts         map[SignalID]SignalReceipt
	childKeys        map[ChildKey]struct{}
	waits            map[WaitKey]Wait
	pending          map[EffectID]PendingCheckpoint
	usage            Usage
	allocated        resourceAmounts
	activeChildren   uint32
	retainedChildren uint32
	paused           bool
	yielded          bool
	terminal         Termination
	exited           *Exited
	stepEffects      []Effect
	execution        Execution
	program          Program[Payload, Payload]
}

func newProcessState(started Started) *processState {
	return &processState{done: make(chan struct{}), started: started, receipts: make(map[SignalID]SignalReceipt), childKeys: make(map[ChildKey]struct{}), waits: make(map[WaitKey]Wait), pending: make(map[EffectID]PendingCheckpoint)}
}

func (p *processState) status() Status {
	switch {
	case p.exited != nil:
		return p.exited.Termination.Status()
	case p.terminal.Valid():
		return StatusDraining
	case p.paused:
		return StatusPaused
	case p.yielded:
		return StatusWaiting
	default:
		return StatusRunning
	}
}

func (p *processState) acceptsControl(kind ControlKind) bool {
	switch kind {
	case ControlPause:
		return p.exited == nil && !p.paused
	case ControlResume:
		return p.exited == nil && p.paused
	default:
		return p.exited == nil && kind.Valid()
	}
}

// unknownEffect reports whether id settled Unknown and awaits resolution.
func (p *processState) unknownEffect(id EffectID) bool {
	pending, ok := p.pending[id]
	return ok && pending.Outcome != nil && pending.Outcome.Status() == SettlementStatusUnknown
}

func (p *processState) ready() bool {
	return p.exited == nil && !p.terminal.Valid() && !p.paused && !p.yielded
}

func (p *processState) admit(signal Signal, limits TreeLimits) error {
	if err := p.admitMessages(limits, 0, nil, 1); err != nil {
		return err
	}
	if !signal.Valid() || p.exited != nil || p.terminal.Valid() {
		return ErrSignalRejected
	}
	digest := signal.digest()
	if receipt, ok := p.receipts[signal.ID]; ok {
		if receipt.Digest == digest {
			return ErrInvalidLog
		}
		return ErrSignalConflict
	}
	if signal.Wait.Valid() {
		wait, ok := p.waits[signal.Wait]
		if !ok {
			return ErrSignalRejected
		}
		if err := wait.admits(signal.Payload); err != nil {
			return err
		}
		delete(p.waits, signal.Wait)
	}
	if p.usage.AcceptedMessages == math.MaxUint64 {
		return ErrCounterExhausted
	}
	p.receipts[signal.ID] = SignalReceipt{ID: signal.ID, Wait: signal.Wait, Digest: digest}
	p.inbox = append(p.inbox, signal)
	p.yielded = false
	p.usage.AcceptedMessages++
	return nil
}

func (p *processState) advance(stepped Stepped, limits TreeLimits) error {
	if stepped.Step != p.step+1 || p.step == math.MaxUint64 || int(stepped.Delivered) > len(p.inbox) || stepped.Transition.Consumed() > stepped.Delivered || p.exited != nil || p.terminal.Valid() {
		return ErrInvalidLog
	}
	transition := stepped.Transition
	if !p.intentsComplete() {
		return ErrInvalidLog
	}
	if err := p.admitTransition(transition, limits, stepped.Delivered); err != nil {
		return err
	}
	p.yielded = transition.kind == TransitionKindYield && int(stepped.Delivered) == len(p.inbox)
	p.inbox = slices.Clone(p.inbox[transition.consumed:])
	p.step = stepped.Step
	p.usage.CommittedSteps++
	p.waits = make(map[WaitKey]Wait, len(transition.waits))
	for _, wait := range transition.waits {
		if !p.answered(wait.Key) {
			p.waits[wait.Key] = wait
		}
	}
	p.stepEffects = transition.Effects()
	switch transition.kind {
	case TransitionKindComplete:
		p.terminal = Termination{output: transition.output}
	case TransitionKindFail:
		p.terminal = Termination{failure: transition.failure}
	}
	return nil
}

func (p *processState) prepare(intent EffectIntent) error {
	if intent.ID.Step != p.step || int(intent.ID.Index) >= len(p.stepEffects) || !p.stepEffects[intent.ID.Index].equal(intent.Effect) {
		return ErrInvalidLog
	}
	if _, ok := p.pending[intent.ID]; ok {
		return ErrInvalidLog
	}
	if !p.started.Capabilities.Allows(intent.Policy.RequiredCapabilities) || p.usage.PreparedEffects == math.MaxUint64 {
		return ErrInvalidEffect
	}
	p.stepEffects[intent.ID.Index] = Effect{}
	p.pending[intent.ID] = PendingCheckpoint{Intent: intent}
	p.usage.PreparedEffects++
	return nil
}

func (p *processState) settle(settled EffectSettled) error {
	pending, ok := p.pending[settled.ID]
	if !ok {
		return ErrEffectNotPending
	}
	if settled.Outcome.Status() == SettlementStatusUnknown {
		pending.Outcome = &settled.Outcome
		pending.Failure = settled.Failure
		p.pending[settled.ID] = pending
		return nil
	}
	if p.usage.AcceptedMessages == math.MaxUint64 {
		return ErrCounterExhausted
	}
	delete(p.pending, settled.ID)
	p.inbox = append(p.inbox, Completion{Effect: settled.ID, Outcome: settled.Outcome})
	p.yielded = false
	p.usage.AcceptedMessages++
	return nil
}

// spawns reports whether a pending Spawn authorizes exactly this child.
func (p *processState) spawns(spec ChildSpec) bool {
	for _, pending := range p.pending {
		if spawn := pending.Intent.Effect.spawn; spawn != nil && spawn.equal(spec) {
			return true
		}
	}
	return false
}

// startedChildren counts every child this process has started, reaped or not,
// because each one permanently claimed a key.
func (p *processState) startedChildren() uint64 { return uint64(len(p.childKeys)) }

func (p *processState) adopted(key ChildKey) bool {
	_, adopted := p.childKeys[key]
	return adopted
}

// adopt debits the child's grant and claims its key for this parent's
// lifetime; a key never names a second child, even after the first is reaped.
func (p *processState) adopt(spec ChildSpec) error {
	debit, _ := p.started.Budget.allocation(spec.Budget)
	allocated, ok := p.allocated.add(debit)
	if !ok {
		return ErrCounterExhausted
	}
	p.allocated = allocated
	p.childKeys[spec.Key] = struct{}{}
	p.activeChildren++
	p.retainedChildren++
	return nil
}

func (p *processState) checkpoint() (ProcessCheckpoint, error) {
	var state ExecutionState
	if !p.terminal.Valid() {
		var err error
		state, err = captureExecution(p.execution)
		if err != nil {
			return ProcessCheckpoint{}, err
		}
	}
	return p.materialize(state), nil
}

func (p *processState) materialize(state ExecutionState) ProcessCheckpoint {
	value := ProcessCheckpoint{Started: p.started, State: state, Step: p.step, Inbox: slices.Clone(p.inbox), Usage: p.usage, Allocated: p.allocated, Paused: p.paused, Yielded: p.yielded, Exited: p.exited}
	if p.terminal.Valid() {
		terminal := p.terminal
		value.Terminal = &terminal
	}
	for _, receipt := range p.receipts {
		value.Receipts = append(value.Receipts, receipt)
	}
	for key := range p.childKeys {
		value.ChildKeys = append(value.ChildKeys, key)
	}
	slices.SortFunc(value.ChildKeys, func(a, b ChildKey) int { return strings.Compare(a.String(), b.String()) })
	slices.SortFunc(value.Receipts, func(a, b SignalReceipt) int { return strings.Compare(a.ID.String(), b.ID.String()) })
	for _, wait := range p.waits {
		value.Waits = append(value.Waits, wait)
	}
	slices.SortFunc(value.Waits, func(a, b Wait) int { return strings.Compare(a.Key.String(), b.Key.String()) })
	for _, pending := range p.pending {
		value.Pending = append(value.Pending, pending)
	}
	slices.SortFunc(value.Pending, func(a, b PendingCheckpoint) int { return strings.Compare(a.Intent.ID.String(), b.Intent.ID.String()) })
	return value
}

func (p *processState) admitTransition(transition Transition, limits TreeLimits, delivered uint32) error {
	if !transition.Valid() || transition.consumed > delivered || int(delivered) > len(p.inbox) {
		return ErrInvalidTransition
	}
	if p.step == math.MaxUint64 || !resourceQuantitiesFit(math.MaxUint64, p.usage.PreparedEffects, uint64(len(transition.effects))) {
		return ErrCounterExhausted
	}
	if !p.started.Budget.Steps.Allows(p.usage.CommittedSteps, p.allocated.Steps, 1) {
		return ErrResourceLimitExceeded
	}
	if !p.started.Budget.Effects.Allows(p.usage.PreparedEffects, p.allocated.Effects, uint64(len(transition.effects))) {
		return ErrResourceLimitExceeded
	}
	if err := p.admitMessages(limits, uint64(transition.consumed), transition.effects, 0); err != nil {
		return err
	}
	if transition.kind == TransitionKindYield && len(transition.effects) == 0 && len(transition.waits) == 0 && p.activeChildren == 0 && len(p.pending) == 0 && len(p.inbox) == int(delivered) {
		return fmt.Errorf("%w: yield has no input or work to await", ErrInvalidTransition)
	}
	for _, wait := range transition.waits {
		if p.answered(wait.Key) && !p.answerAfter(wait.Key, delivered) {
			return fmt.Errorf("%w: answered wait key cannot be reused", ErrInvalidTransition)
		}
	}
	if transition.output.Valid() && p.program.Valid() {
		if err := p.program.descriptor.ValidateOutput(transition.output); err != nil {
			return err
		}
	}
	return nil
}

func (p *processState) result() Result[Payload] {
	if p.exited == nil {
		return Result[Payload]{}
	}
	return Result[Payload]{process: p.started.Process, startedAt: p.started.At, finishedAt: p.exited.At, termination: p.exited.Termination, usage: p.usage}
}

func (p *processState) reservedMessages(effects []Effect) uint64 {
	reserved := uint64(p.activeChildren) + uint64(len(p.pending)) + uint64(len(effects))
	for _, pending := range p.pending {
		if pending.Intent.Effect.spawn != nil {
			reserved++
		}
	}
	for _, effect := range effects {
		if effect.spawn != nil {
			reserved++
		}
	}
	return reserved
}

func (p *processState) admitMessages(limits TreeLimits, consumed uint64, effects []Effect, incoming uint64) error {
	reserved := p.reservedMessages(effects)
	if !resourceQuantitiesFit(math.MaxUint64, p.usage.AcceptedMessages, reserved, incoming) {
		return ErrCounterExhausted
	}
	if !p.started.Budget.Messages.Allows(p.usage.AcceptedMessages, p.allocated.Messages, reserved, incoming) || !resourceQuantitiesFit(limits.MaxPendingMessages, uint64(len(p.inbox))-consumed, reserved, incoming) {
		return ErrResourceLimitExceeded
	}
	return nil
}

func (p *processState) intentsComplete() bool {
	for _, effect := range p.stepEffects {
		if effect.Valid() {
			return false
		}
	}
	return true
}

func (p *processState) answered(wait WaitKey) bool {
	for _, receipt := range p.receipts {
		if receipt.Wait == wait {
			return true
		}
	}
	return false
}

func (p *processState) answerAfter(wait WaitKey, delivered uint32) bool {
	for _, message := range p.inbox[delivered:] {
		if signal, ok := message.(Signal); ok && signal.Wait == wait {
			return true
		}
	}
	return false
}
