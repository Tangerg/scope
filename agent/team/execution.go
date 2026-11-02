package team

import (
	"context"
	"fmt"
	"math"

	"github.com/Tangerg/scope/agent/internal/strategy/stepfail"
	agentrun "github.com/Tangerg/scope/agent/run"
)

type execution struct {
	definition *Definition
	state      executionState
}

func (e *execution) Step(ctx context.Context, signals []agentrun.Signal) (agentrun.Transition, error) {
	if err := ctx.Err(); err != nil {
		return agentrun.Transition{}, err
	}
	switch e.state.phase() {
	case phaseReady:
		if len(signals) != 0 {
			return agentrun.Transition{}, ErrInvalidProtocol
		}
		// The first turn has no prior decision; working state is the initial state.
		return e.startTurn(Decision{}, 0)
	case phaseStartingTurn:
		return e.acceptTurnStart(signals)
	case phaseApplying:
		return e.acceptActions(signals)
	case phaseOpening:
		return e.acceptOpening(signals)
	case phaseWaiting:
		return e.acceptOutcomes(ctx, signals)
	default:
		return agentrun.Transition{}, ErrInvalidProtocol
	}
}

// startTurn opens the next coordinator turn. decision is the current turn's
// already-decoded Decision (the zero Decision for the first turn), so a single
// Step decodes the coordinator output once and threads it here.
func (e *execution) startTurn(decision Decision, consumed uint32) (agentrun.Transition, error) {
	number := e.state.number()
	if !e.definition.maxTurns.Allows(number, 1) {
		return agentrun.Transition{}, ErrTurnLimit
	}
	if number == math.MaxUint64 {
		return agentrun.Transition{}, agentrun.ErrCounterExhausted
	}
	turn := Turn{Number: number + 1, State: e.state.workingState(decision),
		Tasks: append([]Task{}, e.state.Tasks...), Controls: append([]ControlReceipt{}, e.state.Controls...),
		Workers: make([]agentrun.Descriptor, 0, len(e.definition.workers))}
	for _, worker := range e.definition.workers {
		turn.Workers = append(turn.Workers, worker.deployment.Descriptor())
	}
	input, err := agentrun.EncodePayload(turn)
	if err != nil {
		return agentrun.Transition{}, err
	}
	key, err := turnKey(turn.Number)
	if err != nil {
		return agentrun.Transition{}, err
	}
	effect, err := agentrun.NewChildStartEffect(e.definition.coordinator.spec(key, input))
	if err != nil {
		return agentrun.Transition{}, err
	}
	e.state.Turn = &turnExecution{Number: turn.Number, State: turn.State}
	e.state.InitialState = agentrun.Payload{}
	e.state.WaitID = nil
	return agentrun.Continue(consumed, effect)
}

func (e *execution) acceptTurnStart(signals []agentrun.Signal) (agentrun.Transition, error) {
	if len(signals) == 0 {
		return agentrun.Transition{}, ErrInvalidProtocol
	}
	started, err := signals[0].Frame[agentrun.ChildStartResult]()
	if err != nil {
		return agentrun.Transition{}, fmt.Errorf("%w: %w", ErrInvalidProtocol, err)
	}
	batch, err := e.state.batch()
	if err != nil {
		return agentrun.Transition{}, err
	}
	indices, err := batch.AcceptStarts([]agentrun.ChildStartResult{started})
	if err != nil {
		return agentrun.Transition{}, fmt.Errorf("%w: %w", ErrInvalidProtocol, err)
	}
	// A refused turn ends the collaboration; the Runtime owns that Failure.
	if failure, failed := started.Failure(); failed {
		return agentrun.Fail(1, failure)
	}
	e.state.recordStart(indices[0], started)
	return e.openWait(1)
}

func (e *execution) openWait(consumed uint32) (agentrun.Transition, error) {
	e.state.WaitID = nil
	spec, err := e.state.waitSpec()
	if err != nil {
		return agentrun.Transition{}, err
	}
	effect, err := agentrun.NewChildWaitEffect(spec)
	if err != nil {
		return agentrun.Transition{}, err
	}
	return agentrun.Continue(consumed, effect)
}

func (e *execution) acceptOpening(signals []agentrun.Signal) (agentrun.Transition, error) {
	if len(signals) == 0 {
		return agentrun.Transition{}, ErrInvalidProtocol
	}
	opened, err := signals[0].Frame[agentrun.ChildWaitOpened]()
	if err != nil {
		return agentrun.Transition{}, fmt.Errorf("%w: %w", ErrInvalidProtocol, err)
	}
	batch, err := e.state.batch()
	if err != nil {
		return agentrun.Transition{}, err
	}
	id, err := batch.AcceptOpening(opened)
	if err != nil {
		return agentrun.Transition{}, fmt.Errorf("%w: %w", ErrInvalidProtocol, err)
	}
	e.state.WaitID = &id
	return agentrun.Wait(1, id)
}

func (e *execution) acceptOutcomes(ctx context.Context, signals []agentrun.Signal) (agentrun.Transition, error) {
	if len(signals) == 0 {
		return agentrun.Transition{}, ErrInvalidProtocol
	}
	satisfied, err := signals[0].Frame[agentrun.ChildWaitSatisfied]()
	if err != nil {
		return agentrun.Transition{}, fmt.Errorf("%w: %w", ErrInvalidProtocol, err)
	}
	want, err := e.state.waitSpec()
	if err != nil {
		return agentrun.Transition{}, err
	}
	batch, err := e.state.batch()
	if err != nil {
		return agentrun.Transition{}, err
	}
	indices, completionErr := batch.Complete(satisfied, want.Key, want.Boundary, want.Condition)
	if completionErr != nil {
		return agentrun.Transition{}, fmt.Errorf("%w: %w", ErrInvalidProtocol, completionErr)
	}
	decided := e.state.Turn.Outcome != nil
	for offset, outcome := range satisfied.Outcomes() {
		if indices[offset] != len(e.state.Tasks) {
			continue
		}
		// A failed turn ends the collaboration before any decision applies;
		// the Runtime owns that Failure.
		failure, failed, err := turnFailure(outcome)
		if err != nil {
			return agentrun.Transition{}, err
		}
		if failed {
			return agentrun.Fail(1, failure)
		}
	}
	for offset, outcome := range satisfied.Outcomes() {
		e.state.recordOutcome(indices[offset], outcome)
	}
	e.state.WaitID = nil
	if e.state.Turn.Outcome == nil {
		return e.openWait(1)
	}
	decision, decisionErr := e.state.decision()
	if decisionErr != nil {
		return agentrun.Transition{}, decisionErr
	}
	// A turn decided in an earlier Step starts the next turn; one decided by this
	// outcome applies first. Both reuse the one decoded Decision.
	if decided {
		return e.startTurn(decision, 1)
	}
	return e.applyDecision(ctx, decision, 1)
}

// turnFailure reports the Failure a finished coordinator turn ends the
// collaboration with: a failed coordinator, or one whose subtree retains
// unresolved Effects and so cannot authorize a Decision.
func turnFailure(outcome agentrun.ChildOutcome) (agentrun.Failure, bool, error) {
	if !coordinatorOutcomeAuthorizes(outcome) {
		failure, err := stepfail.Failure(agentrun.FailureKindExternal, failureCodeCoordinatorUnresolvedEffects, "Coordinator subtree has unresolved Effects")
		return failure, true, err
	}
	failure, failed := outcome.Result().Termination().Failure()
	return failure, failed, nil
}

func (e *execution) acceptActions(signals []agentrun.Signal) (agentrun.Transition, error) {
	if len(signals) == 0 {
		return agentrun.Transition{}, ErrInvalidProtocol
	}
	consumed, settled, err := e.acceptTaskStarts(signals)
	if err != nil {
		return agentrun.Transition{}, err
	}
	if settled {
		consumed, settled, err = e.acceptControlResults(signals, consumed)
		if err != nil {
			return agentrun.Transition{}, err
		}
	}
	if !settled {
		return agentrun.Continue(consumed)
	}
	decision, err := e.state.decision()
	if err != nil {
		return agentrun.Transition{}, err
	}
	return e.afterActions(decision, consumed)
}

func (e *execution) acceptTaskStarts(signals []agentrun.Signal) (consumed uint32, settled bool, err error) {
	batch, err := e.state.batch()
	if err != nil {
		return 0, false, err
	}
	pending := batch.PendingStarts()
	count := min(pending, len(signals))
	if count == 0 {
		return 0, true, nil
	}
	starts := make([]agentrun.ChildStartResult, count)
	for index := range starts {
		starts[index], err = signals[index].Frame[agentrun.ChildStartResult]()
		if err != nil {
			return 0, false, fmt.Errorf("%w: %w", ErrInvalidProtocol, err)
		}
	}
	indices, err := batch.AcceptStarts(starts)
	if err != nil {
		return 0, false, fmt.Errorf("%w: %w", ErrInvalidProtocol, err)
	}
	for offset, index := range indices {
		e.state.recordStart(index, starts[offset])
	}
	return uint32(count), count == pending, nil
}

func (e *execution) acceptControlResults(signals []agentrun.Signal, consumed uint32) (total uint32, settled bool, err error) {
	for index := range e.state.Controls {
		receipt := &e.state.Controls[index]
		if receipt.Result != nil {
			continue
		}
		if int(consumed) == len(signals) {
			return consumed, false, nil
		}
		result, parseErr := signals[consumed].Frame[agentrun.ChildControlResult]()
		if parseErr != nil {
			return 0, false, fmt.Errorf("%w: %w", ErrInvalidProtocol, parseErr)
		}
		receipt.Result = &result
		consumed++
	}
	return consumed, true, nil
}

func (e *execution) afterActions(decision Decision, consumed uint32) (agentrun.Transition, error) {
	if e.state.awaitsTasks(decision.Mode) {
		return e.openWait(consumed)
	}
	return e.startTurn(decision, consumed)
}

func (e *execution) applyDecision(ctx context.Context, decision Decision, consumed uint32) (agentrun.Transition, error) {
	if err := e.state.validateDecision(ctx, e.definition, decision); err != nil {
		return agentrun.Transition{}, err
	}
	effects := make([]agentrun.Effect, 0, len(decision.Tasks)+len(decision.Controls))
	for _, request := range decision.Tasks {
		worker, _ := e.definition.worker(request.Worker)
		effect, err := agentrun.NewChildStartEffect(worker.spec(request.Key, request.Input))
		if err != nil {
			return agentrun.Transition{}, err
		}
		effects = append(effects, effect)
	}
	tasks := e.state.taskIndex()
	for _, control := range decision.Controls {
		effect, err := control.effect(tasks[control.Task])
		if err != nil {
			return agentrun.Transition{}, err
		}
		effects = append(effects, effect)
	}
	e.state.Controls = nil
	for _, request := range decision.Tasks {
		e.state.Tasks = append(e.state.Tasks, Task{Request: request})
	}
	for _, control := range decision.Controls {
		e.state.Controls = append(e.state.Controls, ControlReceipt{Control: control})
	}
	if decision.completes() {
		e.state = executionState{Completed: true}
		return agentrun.Complete(consumed, decision.Output)
	}
	if len(effects) == 0 {
		return e.afterActions(decision, consumed)
	}
	return agentrun.Continue(consumed, effects...)
}

func (e *execution) Snapshot() (agentrun.ExecutionState, error) {
	return agentrun.EncodeExecutionState(stateKind, e.state)
}

var _ agentrun.Execution = (*execution)(nil)

const failureCodeCoordinatorUnresolvedEffects = "collaboration.coordinator.unresolved_effects"
