package collaboration

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/strategy/internal/childcall"
)

type execution struct {
	definition *Definition
	state      executionState
}

func (e *execution) Step(ctx context.Context, signals []agent.Signal) (agent.Transition, error) {
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
		return e.acceptOutcomes(signals)
	default:
		return agent.Transition{}, ErrInvalidProtocol
	}
}

func (e *execution) startTurn(consumed uint32) (agent.Transition, error) {
	if e.state.Number == e.definition.maxTurns {
		return agent.Transition{}, ErrTurnLimit
	}
	e.state.Number++
	turn := Turn{Number: e.state.Number, State: e.state.State,
		Tasks: append([]Task{}, e.state.Tasks...), Controls: append([]ControlReceipt{}, e.state.Controls...), Workers: []agent.Descriptor{}}
	for _, worker := range e.definition.workers {
		turn.Workers = append(turn.Workers, worker.descriptor)
	}
	input, err := agent.EncodeInput(turn)
	if err != nil {
		return agent.Transition{}, err
	}
	key, err := turnKey(e.state.Number)
	if err != nil {
		return agent.Transition{}, err
	}
	effect, err := agent.StartChild(e.definition.coordinator.spec(key, input))
	if err != nil {
		return agent.Transition{}, err
	}
	e.state.Turn = &turnExecution{Input: turn}
	e.state.Mode = ""
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
		return agent.Transition{}, err
	}
	key, err := turnKey(e.state.Number)
	if err != nil {
		return agent.Transition{}, err
	}
	if !childcall.StartMatches(started, key, e.definition.coordinator.deploymentRef) {
		return agent.Transition{}, ErrInvalidProtocol
	}
	e.state.Turn.Start = &started
	if failure, failed := started.Failure(); failed {
		e.state.Phase = phaseFailed
		return agent.Fail(1, failure)
	}
	return e.openWait(1)
}

func (e *execution) openWait(consumed uint32) (agent.Transition, error) {
	e.state.WaitSequence++
	e.state.WaitID = nil
	spec, err := e.state.waitSpec()
	if err != nil {
		return agent.Transition{}, err
	}
	effect, err := agent.WaitForChildren(spec)
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
		return agent.Transition{}, err
	}
	want, err := e.state.waitSpec()
	if err != nil {
		return agent.Transition{}, err
	}
	if !childcall.OpeningMatches(opened, want) {
		return agent.Transition{}, ErrInvalidProtocol
	}
	id := opened.WaitID()
	e.state.WaitID = &id
	e.state.Phase = phaseWaiting
	return agent.Wait(1, id)
}

func (e *execution) acceptOutcomes(signals []agent.Signal) (agent.Transition, error) {
	if len(signals) == 0 {
		return agent.Transition{}, ErrInvalidProtocol
	}
	satisfied, err := agent.ParseChildWaitSatisfied(signals[0])
	if err != nil {
		return agent.Transition{}, err
	}
	want, err := e.state.waitSpec()
	if err != nil {
		return agent.Transition{}, err
	}
	if e.state.WaitID == nil || !childcall.CompletionMatches(satisfied, *e.state.WaitID, want.Key, want.Boundary) {
		return agent.Transition{}, ErrInvalidProtocol
	}
	outcomes := satisfied.Outcomes()
	last := -1
	for _, outcome := range outcomes {
		index := slices.Index(want.Children, outcome.Result().ProcessID())
		if index <= last || !e.state.recordOutcome(outcome) {
			return agent.Transition{}, ErrInvalidProtocol
		}
		last = index
	}
	if len(outcomes) == 0 {
		return agent.Transition{}, ErrInvalidProtocol
	}
	e.state.WaitID = nil
	if e.state.Turn.Outcome == nil {
		return e.openWait(1)
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
		return agent.Transition{}, fmt.Errorf("collaboration: coordinator ended with %s: %s", result.Status(), result.Termination().Reason())
	}
	decision, err := output.Decode[Decision]()
	if err != nil {
		return agent.Transition{}, fmt.Errorf("%w: %w", ErrInvalidDecision, err)
	}
	return e.applyDecision(decision, 1)
}

func (e *execution) acceptActions(signals []agent.Signal) (agent.Transition, error) {
	if len(signals) == 0 {
		return agent.Transition{}, ErrInvalidProtocol
	}
	var consumed uint32
	for index := range e.state.Tasks {
		task := &e.state.Tasks[index]
		if task.Start != nil {
			continue
		}
		if int(consumed) == len(signals) {
			return agent.Continue(consumed)
		}
		started, err := agent.ParseChildStartResult(signals[consumed])
		if err != nil {
			return agent.Transition{}, err
		}
		worker, _ := e.definition.worker(task.Request.Worker)
		if !childcall.StartMatches(started, task.Request.Key, worker.deploymentRef) {
			return agent.Transition{}, ErrInvalidProtocol
		}
		task.Start = &started
		consumed++
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
			return agent.Transition{}, err
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
	if e.state.Mode == Wait && len(e.state.remaining()) != 0 {
		return e.openWait(consumed)
	}
	return e.startTurn(consumed)
}

func (e *execution) applyDecision(decision Decision, consumed uint32) (agent.Transition, error) {
	if err := e.state.validateDecision(e.definition, decision); err != nil {
		return agent.Transition{}, err
	}
	effects := make([]agent.Effect, 0, len(decision.Tasks)+len(decision.Controls))
	for _, request := range decision.Tasks {
		worker, _ := e.definition.worker(request.Worker)
		effect, err := agent.StartChild(worker.spec(request.Key, request.Input))
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
	payload, err := json.Marshal(e.state)
	if err != nil {
		return agent.ExecutionState{}, err
	}
	return agent.NewExecutionState(stateKind, payload)
}
