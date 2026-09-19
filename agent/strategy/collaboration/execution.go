package collaboration

import (
	"context"
	"errors"
	"fmt"

	agent "github.com/Tangerg/scope/agent"
)

type execution struct {
	definition *Definition
	state      executionState
}

func (e *execution) Step(ctx context.Context, signals []agent.Signal) (agent.Transition, error) {
	transition, err := e.step(ctx, signals)
	if err == nil {
		return transition, nil
	}
	var kind agent.FailureKind
	var code string
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return agent.Transition{}, err
	case errors.Is(err, ErrTurnLimit):
		kind, code = agent.FailureKindExecution, failureCodeCollaborationLimitTurns
	case errors.Is(err, agent.ErrCounterExhausted):
		kind, code = agent.FailureKindExecution, failureCodeCollaborationCounterExhausted
	case errors.Is(err, ErrInvalidDecision):
		kind, code = agent.FailureKindContract, failureCodeCollaborationDecisionInvalid
	case errors.Is(err, ErrInvalidProtocol):
		kind, code = agent.FailureKindContract, failureCodeCollaborationProtocolInvalid
	default:
		return agent.Transition{}, err
	}
	failure, failureErr := agent.NewFailure(kind, code, agent.NormalizeDiagnostic(err.Error()))
	if failureErr != nil {
		return agent.Transition{}, failureErr
	}
	return agent.Transition{}, &agent.StepError{Failure: failure, Cause: err}
}

func (e *execution) step(ctx context.Context, signals []agent.Signal) (agent.Transition, error) {
	if err := ctx.Err(); err != nil {
		return agent.Transition{}, err
	}
	switch e.state.Phase {
	case phaseReady:
		if len(signals) != 0 {
			return agent.Transition{}, ErrInvalidProtocol
		}
		return e.startTurn(0)
	case phaseStartingTurn:
		return e.acceptTurnStart(signals)
	case phaseApplying:
		return e.acceptActions(signals)
	case phaseOpening:
		return e.acceptOpening(signals)
	case phaseWaiting:
		return e.acceptOutcomes(ctx, signals)
	default:
		return agent.Transition{}, ErrInvalidProtocol
	}
}

func (e *execution) startTurn(consumed uint32) (agent.Transition, error) {
	if !e.definition.maxTurns.Allows(e.state.Number, 1) {
		return agent.Transition{}, ErrTurnLimit
	}
	if e.state.Number == ^uint64(0) {
		return agent.Transition{}, agent.ErrCounterExhausted
	}
	e.state.Number++
	turn := Turn{Number: e.state.Number, State: e.state.State,
		Tasks: append([]Task{}, e.state.Tasks...), Controls: append([]ControlReceipt{}, e.state.Controls...), Workers: []agent.Descriptor{}}
	for _, worker := range e.definition.workers {
		turn.Workers = append(turn.Workers, worker.descriptor)
	}
	input, err := agent.EncodePayload(turn)
	if err != nil {
		return agent.Transition{}, err
	}
	key, err := turnKey(e.state.Number)
	if err != nil {
		return agent.Transition{}, err
	}
	effect, err := agent.NewChildStartEffect(e.definition.coordinator.spec(key, input))
	if err != nil {
		return agent.Transition{}, err
	}
	e.state.Turn = &turnExecution{Input: turn}
	e.state.Mode = Undecided
	e.state.WaitID = nil
	e.state.Phase = phaseStartingTurn
	return agent.Continue(consumed, effect)
}

func (e *execution) acceptTurnStart(signals []agent.Signal) (agent.Transition, error) {
	if len(signals) == 0 {
		return agent.Transition{}, ErrInvalidProtocol
	}
	started, err := agent.ParseChildStartResult(signals[0])
	if err != nil {
		return agent.Transition{}, fmt.Errorf("%w: %w", ErrInvalidProtocol, err)
	}
	batch, err := e.state.batch(e.definition)
	if err != nil {
		return agent.Transition{}, err
	}
	indices, err := batch.AcceptStarts([]agent.ChildStartResult{started})
	if err != nil {
		return agent.Transition{}, fmt.Errorf("%w: %w", ErrInvalidProtocol, err)
	}
	e.state.recordStart(indices[0], started)
	if failure, failed := started.Failure(); failed {
		e.state.Phase = phaseFailed
		return agent.Fail(1, failure)
	}
	return e.openWait(1)
}

func (e *execution) openWait(consumed uint32) (agent.Transition, error) {
	if e.state.WaitSequence == ^uint64(0) {
		return agent.Transition{}, agent.ErrCounterExhausted
	}
	e.state.WaitSequence++
	e.state.WaitID = nil
	spec, err := e.state.waitSpec(e.definition)
	if err != nil {
		return agent.Transition{}, err
	}
	effect, err := agent.NewChildWaitEffect(spec)
	if err != nil {
		return agent.Transition{}, err
	}
	e.state.Phase = phaseOpening
	return agent.Continue(consumed, effect)
}

func (e *execution) acceptOpening(signals []agent.Signal) (agent.Transition, error) {
	if len(signals) == 0 {
		return agent.Transition{}, ErrInvalidProtocol
	}
	opened, err := agent.ParseChildWaitOpened(signals[0])
	if err != nil {
		return agent.Transition{}, fmt.Errorf("%w: %w", ErrInvalidProtocol, err)
	}
	want, err := e.state.waitSpec(e.definition)
	if err != nil {
		return agent.Transition{}, err
	}
	batch, err := e.state.batch(e.definition)
	if err != nil {
		return agent.Transition{}, err
	}
	id, err := batch.AcceptOpening(opened, want.Key, want.Boundary, want.Condition)
	if err != nil {
		return agent.Transition{}, fmt.Errorf("%w: %w", ErrInvalidProtocol, err)
	}
	e.state.WaitID = &id
	e.state.Phase = phaseWaiting
	return agent.Wait(1, id)
}

func (e *execution) acceptOutcomes(ctx context.Context, signals []agent.Signal) (agent.Transition, error) {
	if len(signals) == 0 {
		return agent.Transition{}, ErrInvalidProtocol
	}
	satisfied, err := agent.ParseChildWaitSatisfied(signals[0])
	if err != nil {
		return agent.Transition{}, fmt.Errorf("%w: %w", ErrInvalidProtocol, err)
	}
	want, err := e.state.waitSpec(e.definition)
	if err != nil {
		return agent.Transition{}, err
	}
	batch, err := e.state.batch(e.definition)
	if err != nil {
		return agent.Transition{}, err
	}
	indices, completionErr := batch.Complete(satisfied, want.Key, want.Boundary, want.Condition)
	if completionErr != nil {
		return agent.Transition{}, fmt.Errorf("%w: %w", ErrInvalidProtocol, completionErr)
	}
	for offset, outcome := range satisfied.Outcomes() {
		e.state.recordOutcome(indices[offset], outcome)
	}
	e.state.WaitID = nil
	if e.state.Turn.Outcome == nil {
		return e.openWait(1)
	}
	if e.state.Turn.unresolved() {
		failure, failureErr := agent.NewFailure(agent.FailureKindExternal, failureCodeCollaborationCoordinatorUnresolvedEffects, "Coordinator subtree has unresolved Effects")
		if failureErr != nil {
			return agent.Transition{}, failureErr
		}
		e.state.Phase = phaseFailed
		return agent.Fail(1, failure)
	}
	if e.state.Mode == Wait {
		return e.startTurn(1)
	}
	result := e.state.Turn.Outcome.Result()
	if failure, failed := result.Termination().Failure(); failed {
		e.state.Phase = phaseFailed
		return agent.Fail(1, failure)
	}
	output, present := result.Output()
	if !present || result.Status() != agent.StatusCompleted {
		return agent.Transition{}, fmt.Errorf("%w: coordinator ended with %s: %s", ErrInvalidDecision, result.Status(), result.Termination().Reason())
	}
	decision, err := output.Decode[Decision]()
	if err != nil {
		return agent.Transition{}, fmt.Errorf("%w: %w", ErrInvalidDecision, err)
	}
	return e.applyDecision(ctx, decision, 1)
}

func (e *execution) acceptActions(signals []agent.Signal) (agent.Transition, error) {
	if len(signals) == 0 {
		return agent.Transition{}, ErrInvalidProtocol
	}
	batch, err := e.state.batch(e.definition)
	if err != nil {
		return agent.Transition{}, err
	}
	count := min(batch.PendingStarts(), len(signals))
	starts := make([]agent.ChildStartResult, count)
	for index := range starts {
		started, err := agent.ParseChildStartResult(signals[index])
		if err != nil {
			return agent.Transition{}, fmt.Errorf("%w: %w", ErrInvalidProtocol, err)
		}
		starts[index] = started
	}
	if count > 0 {
		indices, err := batch.AcceptStarts(starts)
		if err != nil {
			return agent.Transition{}, fmt.Errorf("%w: %w", ErrInvalidProtocol, err)
		}
		for offset, index := range indices {
			e.state.recordStart(index, starts[offset])
		}
	}
	consumed := uint32(count)
	if count < batch.PendingStarts() {
		return agent.Continue(consumed)
	}
	for index := range e.state.Controls {
		receipt := &e.state.Controls[index]
		if receipt.Result != nil {
			continue
		}
		if int(consumed) == len(signals) {
			return agent.Continue(consumed)
		}
		result, err := agent.ParseChildControlResult(signals[consumed])
		if err != nil {
			return agent.Transition{}, fmt.Errorf("%w: %w", ErrInvalidProtocol, err)
		}
		effect, err := e.state.controlEffect(receipt.Control)
		if err != nil || !result.Matches(effect) {
			return agent.Transition{}, ErrInvalidProtocol
		}
		receipt.Result = &result
		consumed++
	}
	return e.afterActions(consumed)
}

func (e *execution) afterActions(consumed uint32) (agent.Transition, error) {
	if e.state.Mode == Wait && !e.state.hasUnseenOutcome() && len(e.state.remaining()) != 0 {
		return e.openWait(consumed)
	}
	return e.startTurn(consumed)
}

func (e *execution) applyDecision(ctx context.Context, decision Decision, consumed uint32) (agent.Transition, error) {
	if err := e.state.validateDecision(ctx, e.definition, decision); err != nil {
		return agent.Transition{}, err
	}
	effects := make([]agent.Effect, 0, len(decision.Tasks)+len(decision.Controls))
	for _, request := range decision.Tasks {
		worker, _ := e.definition.worker(request.Worker)
		effect, err := agent.NewChildStartEffect(worker.spec(request.Key, request.Input))
		if err != nil {
			return agent.Transition{}, err
		}
		effects = append(effects, effect)
	}
	for _, control := range decision.Controls {
		effect, err := e.state.controlEffect(control)
		if err != nil {
			return agent.Transition{}, err
		}
		effects = append(effects, effect)
	}
	e.state.State = decision.State
	e.state.Mode = decision.Mode
	e.state.Controls = nil
	for _, request := range decision.Tasks {
		e.state.Tasks = append(e.state.Tasks, Task{Request: request})
	}
	for _, control := range decision.Controls {
		e.state.Controls = append(e.state.Controls, ControlReceipt{Control: control})
	}
	if decision.Mode == Complete {
		e.state.Phase = phaseCompleted
		e.state.Output = decision.Output
		return agent.Complete(consumed, *decision.Output)
	}
	if len(effects) == 0 {
		return e.afterActions(consumed)
	}
	e.state.Phase = phaseApplying
	return agent.Continue(consumed, effects...)
}

func (e *execution) Snapshot() (agent.ExecutionState, error) {
	return agent.EncodeExecutionState(stateKind, e.state)
}

var _ agent.Execution = (*execution)(nil)

const (
	failureCodeCollaborationLimitTurns                   = "collaboration.limit.turns"
	failureCodeCollaborationCounterExhausted             = "collaboration.counter.exhausted"
	failureCodeCollaborationDecisionInvalid              = "collaboration.decision.invalid"
	failureCodeCollaborationProtocolInvalid              = "collaboration.protocol.invalid"
	failureCodeCollaborationCoordinatorUnresolvedEffects = "collaboration.coordinator.unresolved_effects"
)
