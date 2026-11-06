package planning

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"

	"github.com/Tangerg/scope/agent/internal/strategy/child"
	"github.com/Tangerg/scope/agent/internal/strategy/stepfail"
	"github.com/Tangerg/scope/agent/run"
)

const (
	failureCodePlanningDispatchRejected = "planning.dispatch.rejected"
	actionChildKeyPrefix                = "planning.action."
)

type execution struct {
	definition *definition
	state      executionState
}

func (e *execution) Step(ctx context.Context, turn run.Turn) (run.Transition, error) {
	if e == nil || !e.definition.valid() {
		return run.Transition{}, ErrInvalidExecutionState
	}
	if err := ctx.Err(); err != nil {
		return run.Transition{}, err
	}
	switch e.state.phase(e.definition) {
	case phaseReadySense:
		if len(turn.Messages) != 0 {
			return run.Transition{}, fmt.Errorf("%w: initial sensing does not accept Signals", ErrInvalidProtocol)
		}
		return e.requestSense(turn, 0)
	case phaseAwaitingSense:
		return e.acceptSense(ctx, turn)
	case phaseAwaitingAction:
		return e.acceptAction(turn)
	case phaseChild:
		return e.advanceChild(turn)
	case phaseCompleted:
		return run.Transition{}, fmt.Errorf("%w: completed execution cannot advance", ErrInvalidExecutionState)
	default:
		return run.Transition{}, ErrInvalidExecutionState
	}
}

func (e *execution) Snapshot() (run.ExecutionState, error) {
	return e.state.snapshot()
}

func (e *execution) requestSense(turn run.Turn, consumedMessages uint32) (run.Transition, error) {
	input, err := e.state.input()
	if err != nil {
		return run.Transition{}, err
	}
	effect, err := newSenseEffect(input)
	if err != nil {
		return run.Transition{}, err
	}
	e.state.PendingEffect = new(turn.EffectID(0))
	e.state.Phase = phaseAwaitingSense
	return run.Yield(consumedMessages, []run.Effect{effect})
}

func (e *execution) acceptSense(ctx context.Context, turn run.Turn) (run.Transition, error) {
	sensed, failure, err := decodeSettlement[senseResult](turn.Messages, e.state.PendingEffect)
	if err != nil {
		return run.Transition{}, err
	}
	if failure != nil {
		if failure.Kind() == run.FailureKindContract {
			return stepfail.Transition(1, run.FailureKindContract, failureCodePlanningDispatchRejected, failure.Message())
		}
		return stepfail.Transition(1, run.FailureKindExternal, failureCodePlanningSensingFailed, failure.Message())
	}
	e.state.WorldState = sensed.WorldState
	if e.state.awaitingConfirmation() {
		binding, found := e.definition.binding(e.state.CurrentActionName)
		if !found {
			return run.Transition{}, ErrInvalidExecutionState
		}
		e.state.confirmAction(binding.action)
	}
	return e.decide(ctx, turn, 1)
}

func (e *execution) decide(ctx context.Context, turn run.Turn, consumedMessages uint32) (run.Transition, error) {
	if !e.state.needsPlan(e.definition) {
		return e.complete(ctx, consumedMessages)
	}
	problem := e.definition.problem(e.state)
	plan, found, err := e.definition.planner.Plan(ctx, problem)
	if cancelErr := ctx.Err(); cancelErr != nil {
		return run.Transition{}, cancelErr
	}
	if err != nil {
		return e.failPlanning(consumedMessages, run.FailureKindExecution, failureCodePlanningPlannerFailed, err)
	}
	if !found {
		return e.complete(ctx, consumedMessages)
	}
	if _, err := problem.EvaluatePlan(ctx, plan); err != nil {
		return e.failPlanning(consumedMessages, run.FailureKindContract, failureCodePlanningPlannerContract, err)
	}
	binding, found := e.definition.binding(plan.actions[0].name)
	if !found {
		return stepfail.Transition(
			consumedMessages, run.FailureKindContract, failureCodePlanningPlannerContract,
			"Planner selected an Action outside the Planning definition",
		)
	}
	return e.startAction(turn, consumedMessages, binding)
}

// failPlanning keeps cancellation a Step error rather than a Planner Failure.
func (e *execution) failPlanning(consumedMessages uint32, kind run.FailureKind, code string, err error) (run.Transition, error) {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return run.Transition{}, err
	}
	return stepfail.Transition(consumedMessages, kind, code, err.Error())
}

func (e *execution) startAction(turn run.Turn, consumedMessages uint32, binding actionDeclaration) (run.Transition, error) {
	input, err := e.state.input()
	if err != nil {
		return run.Transition{}, err
	}
	if binding.delegatesToChild() {
		return e.startChild(turn, consumedMessages, binding, input)
	}
	effect, err := newActionEffect(input, binding, e.state.WorldState)
	if err != nil {
		return run.Transition{}, err
	}
	e.state.PendingEffect = new(turn.EffectID(0))
	e.state.CurrentActionName = binding.action.name
	e.state.Phase = phaseAwaitingAction
	return run.Yield(consumedMessages, []run.Effect{effect})
}

func (e *execution) startChild(turn run.Turn, consumedMessages uint32, binding actionDeclaration, input run.Payload) (run.Transition, error) {
	childInput := input
	if binding.childInput != nil {
		var err error
		childInput, err = binding.childInput(input, e.state.WorldState)
		if err != nil {
			return stepfail.Transition(consumedMessages, run.FailureKindContract, failureCodePlanningChildInputFailed, err.Error())
		}
	}
	if !childInput.Valid() {
		return stepfail.Transition(
			consumedMessages, run.FailureKindContract, failureCodePlanningChildInputInvalid,
			"Child input function returned an invalid Input",
		)
	}
	key, err := planningChildKey(binding.action.name, uint64(len(e.state.Attempts))+1)
	if err != nil {
		return run.Transition{}, err
	}
	spec, err := binding.child.Spec(key, childInput)
	if err != nil {
		return run.Transition{}, err
	}
	invocation, effect, err := child.Start(turn, 0, spec)
	if err != nil {
		return run.Transition{}, err
	}
	e.state.PendingEffect = nil
	e.state.Child = &invocation
	e.state.CurrentActionName = binding.action.name
	e.state.Phase = phaseAwaitingAction
	return run.Yield(consumedMessages, []run.Effect{effect})
}

func (e *execution) acceptAction(turn run.Turn) (run.Transition, error) {
	_, failure, err := decodeSettlement[actionCompleted](turn.Messages, e.state.PendingEffect)
	if err != nil {
		return run.Transition{}, err
	}
	if failure != nil {
		if failure.Kind() == run.FailureKindContract {
			return stepfail.Transition(1, run.FailureKindContract, failureCodePlanningDispatchRejected, failure.Message())
		}
		e.state.recordFailedAction(failure.Message())
	}
	return e.requestSense(turn, 1)
}

func (e *execution) advanceChild(turn run.Turn) (run.Transition, error) {
	var consumed uint32
	for _, message := range turn.Messages {
		accepted, err := e.state.Child.Accept(message)
		if err != nil {
			return run.Transition{}, err
		}
		if !accepted {
			return run.Transition{}, ErrInvalidProtocol
		}
		consumed++
		if e.state.Child.Done() {
			break
		}
	}
	if !e.state.Child.Done() {
		return run.Yield(consumed, nil)
	}
	if failure := e.state.Child.Rejection; failure != nil {
		e.state.recordFailedAction(failure.Code() + ": " + failure.Message())
	} else if outcome := e.state.Child.Termination; outcome.Status() != run.StatusCompleted {
		e.state.recordFailedAction(outcome.Reason())
	}
	e.state.Child = nil
	return e.requestSense(turn, consumed)
}

func (e *execution) complete(ctx context.Context, consumedMessages uint32) (run.Transition, error) {
	output, err := e.state.complete(ctx, e.definition)
	if err != nil {
		return run.Transition{}, err
	}
	erased, err := run.EncodePayload(output)
	if err != nil {
		return run.Transition{}, err
	}
	return run.Complete(consumedMessages, erased)
}

func planningChildKey(action string, attempt uint64) (run.ChildKey, error) {
	return run.ParseChildKey(actionChildKeyPrefix + planningIdentity(action, attempt))
}

func planningIdentity(action string, attempt uint64) string {
	hash := sha256.New()
	hash.Write([]byte(action))
	hash.Write([]byte{0})
	hash.Write([]byte(strconv.FormatUint(attempt, 10)))
	return hex.EncodeToString(hash.Sum(nil))
}

const (
	failureCodePlanningChildInputFailed  = "planning.child.input.failed"
	failureCodePlanningChildInputInvalid = "planning.child.input.invalid"
	failureCodePlanningPlannerContract   = "planning.planner.contract"
	failureCodePlanningPlannerFailed     = "planning.planner.failed"
	failureCodePlanningSensingFailed     = "planning.sensing.failed"
)
