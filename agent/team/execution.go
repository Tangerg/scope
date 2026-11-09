package team

import (
	"context"
	"fmt"
	"math"

	"github.com/Tangerg/scope/agent/internal/strategy/child"
	"github.com/Tangerg/scope/agent/internal/strategy/stepfail"
	run "github.com/Tangerg/scope/agent/run"
)

const failureCodeCoordinatorNotCompleted = "team.coordinator.not_completed"

type execution struct {
	definition *definition
	state      executionState
}

func (e *execution) Snapshot() (run.ExecutionState, error) {
	return run.EncodeExecutionState(stateKind, e.state)
}
func (e *execution) Step(ctx context.Context, turn run.Turn) (run.Transition, error) {
	if err := ctx.Err(); err != nil {
		return run.Transition{}, err
	}
	for _, message := range turn.Messages {
		if err := e.accept(message); err != nil {
			return run.Transition{}, err
		}
	}
	consumed := uint32(len(turn.Messages))
	if e.state.Coordinator == nil {
		return e.advance(turn, consumed)
	}
	return e.followCoordinator(ctx, turn, consumed)
}

// followCoordinator waits for the current turn's coordinator and applies its
// decision; a coordinator that ends without one ends the collaboration.
func (e *execution) followCoordinator(ctx context.Context, turn run.Turn, consumed uint32) (run.Transition, error) {
	invocation := e.state.Coordinator
	if invocation.Rejection != nil {
		return run.Fail(consumed, *invocation.Rejection)
	}
	if invocation.Termination == nil {
		return run.Yield(consumed, nil)
	}
	termination := *invocation.Termination
	if failure, failed := termination.Failure(); failed {
		return run.Fail(consumed, failure)
	}
	output, completed := termination.Output()
	if !completed {
		return stepfail.Transition(consumed, run.FailureKindExternal, failureCodeCoordinatorNotCompleted, termination.Reason())
	}
	decision, err := output.Decode[Decision]()
	if err != nil {
		return run.Transition{}, err
	}
	return e.applyDecision(ctx, turn, decision, consumed)
}
func (e *execution) accept(message run.Message) error {
	if e.state.Coordinator != nil && !e.state.Coordinator.Done() {
		matched, err := e.state.Coordinator.Accept(message)
		if err != nil {
			return err
		}
		if matched {
			return nil
		}
	}
	for index := range e.state.Tasks {
		invocation := &e.state.Tasks[index].Invocation
		if invocation.Done() {
			continue
		}
		matched, err := invocation.Accept(message)
		if err != nil {
			return err
		}
		if matched {
			if invocation.Termination != nil {
				e.state.OutcomeArrived = true
			}
			return nil
		}
	}
	if completion, ok := message.(run.Completion); ok {
		for index := range e.state.Controls {
			control := &e.state.Controls[index]
			if control.Effect != nil && *control.Effect == completion.Effect {
				control.Effect = nil
				control.Result = &completion.Outcome
				return nil
			}
		}
	}
	return fmt.Errorf("%w: message answers no outstanding team operation", ErrInvalidProtocol)
}
func (e *execution) advance(turn run.Turn, consumed uint32) (run.Transition, error) {
	if !e.state.nextTurnDue() {
		return run.Yield(consumed, nil)
	}
	return e.startTurn(turn, consumed)
}
func (e *execution) startTurn(runtimeTurn run.Turn, consumed uint32) (run.Transition, error) {
	if !e.definition.maxTurns.Allows(e.state.TurnNumber, 1) {
		return run.Transition{}, ErrTurnLimit
	}
	if e.state.TurnNumber == math.MaxUint64 {
		return run.Transition{}, run.ErrCounterExhausted
	}
	number := e.state.TurnNumber + 1
	input := Turn{Number: number, State: e.state.WorkingState, Tasks: make([]Task, len(e.state.Tasks)), Controls: make([]ControlReceipt, len(e.state.Controls)), Workers: make([]run.Descriptor, len(e.definition.workers))}
	for index, task := range e.state.Tasks {
		input.Tasks[index] = task.project()
	}
	for index, control := range e.state.Controls {
		input.Controls[index] = control.project()
	}
	for index, worker := range e.definition.workers {
		input.Workers[index] = worker.Program.Descriptor()
	}
	payload, err := run.EncodePayload(input)
	if err != nil {
		return run.Transition{}, err
	}
	key, err := turnKey(number)
	if err != nil {
		return run.Transition{}, err
	}
	spec, err := e.definition.coordinator.Spec(key, payload)
	if err != nil {
		return run.Transition{}, err
	}
	invocation, effect, err := child.Start(runtimeTurn, 0, spec)
	if err != nil {
		return run.Transition{}, err
	}
	e.state.TurnNumber = number
	e.state.Coordinator = &invocation
	e.state.WaitForTask = false
	e.state.OutcomeArrived = false
	return run.Yield(consumed, []run.Effect{effect})
}
func (e *execution) applyDecision(ctx context.Context, turn run.Turn, decision Decision, consumed uint32) (run.Transition, error) {
	if err := e.state.validateDecision(ctx, e.definition, turn.Process, decision); err != nil {
		return run.Transition{}, err
	}
	if decision.completes() {
		return run.Complete(consumed, decision.Output)
	}
	tasks := e.state.taskIndex()
	effects := make([]run.Effect, 0, len(decision.Tasks)+len(decision.Controls))
	for _, request := range decision.Tasks {
		worker, _ := e.definition.worker(request.Worker)
		spec, err := worker.Spec(request.Key, request.Input)
		if err != nil {
			return run.Transition{}, err
		}
		invocation, effect, err := child.Start(turn, uint32(len(effects)), spec)
		if err != nil {
			return run.Transition{}, err
		}
		e.state.Tasks = append(e.state.Tasks, taskState{Request: request, Invocation: invocation})
		effects = append(effects, effect)
	}
	e.state.Controls = nil
	for _, control := range decision.Controls {
		effect, err := control.effect(turn.Process, tasks[control.Task])
		if err != nil {
			return run.Transition{}, err
		}
		e.state.Controls = append(e.state.Controls, controlState{Control: control, Effect: new(turn.EffectID(uint32(len(effects))))})
		effects = append(effects, effect)
	}
	e.state.Coordinator = nil
	e.state.WorkingState = decision.State
	e.state.WaitForTask = decision.Mode == ModeWait
	if len(effects) != 0 {
		return run.Yield(consumed, effects)
	}
	return e.advance(turn, consumed)
}
