package planning

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/strategy/internal/childcall"
	"github.com/Tangerg/scope/agent/strategy/internal/stepfail"
)

const failureCodePlanningDispatchRejected = "planning.dispatch.rejected"

type execution struct {
	definition *Definition
	state      executionState
}

func (e *execution) Step(ctx context.Context, signals []agent.Signal) (agent.Transition, error) {
	if e == nil || !e.definition.valid() {
		return agent.Transition{}, ErrInvalidExecutionState
	}
	if err := ctx.Err(); err != nil {
		return agent.Transition{}, err
	}
	switch e.state.phase(e.definition) {
	case phaseReadySense:
		if len(signals) != 0 {
			return agent.Transition{}, fmt.Errorf("%w: initial sensing does not accept Signals", ErrInvalidProtocol)
		}
		return e.requestSense(0)
	case phaseAwaitingSense:
		return e.acceptSense(ctx, signals)
	case phaseAwaitingAction:
		return e.acceptAction(signals)
	case phaseChild:
		return e.advanceChild(signals)
	case phaseCompleted:
		return agent.Transition{}, fmt.Errorf("%w: completed execution cannot advance", ErrInvalidExecutionState)
	default:
		return agent.Transition{}, ErrInvalidExecutionState
	}
}

func (e *execution) Snapshot() (agent.ExecutionState, error) {
	return e.state.snapshot()
}

func (e *execution) requestSense(consumedSignals uint32) (agent.Transition, error) {
	input, err := e.state.input()
	if err != nil {
		return agent.Transition{}, err
	}
	effect, err := newSenseEffect(input)
	if err != nil {
		return agent.Transition{}, err
	}
	e.state.Phase = phaseAwaitingSense
	return agent.Continue(consumedSignals, effect)
}

func (e *execution) acceptSense(ctx context.Context, signals []agent.Signal) (agent.Transition, error) {
	sensed, failure, err := decodeSettlement[senseResult](signals)
	if err != nil {
		return agent.Transition{}, err
	}
	if failure != nil {
		if failure.HostError != "" {
			return stepfail.Transition(1, agent.FailureKindContract, failureCodePlanningDispatchRejected, failure.HostError)
		}
		return stepfail.Transition(1, agent.FailureKindExternal, failureCodePlanningSensingFailed, failure.Diagnostic)
	}
	e.state.WorldState = sensed.WorldState
	if e.state.awaitingConfirmation() {
		binding, found := e.definition.binding(e.state.CurrentActionName)
		if !found {
			return agent.Transition{}, ErrInvalidExecutionState
		}
		e.state.confirmAction(binding.action)
	}
	return e.decide(ctx, 1)
}

func (e *execution) decide(ctx context.Context, consumedSignals uint32) (agent.Transition, error) {
	if !e.state.needsPlan(e.definition) {
		return e.complete(ctx, consumedSignals)
	}
	problem := e.definition.problem(e.state)
	plan, found, err := e.definition.planner.Plan(ctx, problem)
	if cancelErr := ctx.Err(); cancelErr != nil {
		return agent.Transition{}, cancelErr
	}
	if err != nil {
		return e.failPlanning(consumedSignals, agent.FailureKindExecution, failureCodePlanningPlannerFailed, err)
	}
	if !found {
		return e.complete(ctx, consumedSignals)
	}
	if _, err := problem.EvaluatePlan(ctx, plan); err != nil {
		return e.failPlanning(consumedSignals, agent.FailureKindContract, failureCodePlanningPlannerContract, err)
	}
	binding, found := e.definition.binding(plan.actions[0].name)
	if !found {
		return stepfail.Transition(
			consumedSignals, agent.FailureKindContract, failureCodePlanningPlannerContract,
			"Planner selected an Action outside the Planning Definition",
		)
	}
	return e.startAction(consumedSignals, binding)
}

// failPlanning keeps cancellation a Step error rather than a Planner Failure.
func (e *execution) failPlanning(consumedSignals uint32, kind agent.FailureKind, code string, err error) (agent.Transition, error) {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return agent.Transition{}, err
	}
	return stepfail.Transition(consumedSignals, kind, code, err.Error())
}

func (e *execution) startAction(consumedSignals uint32, binding ActionBinding) (agent.Transition, error) {
	input, err := e.state.input()
	if err != nil {
		return agent.Transition{}, err
	}
	if binding.delegatesToChild() {
		return e.startChild(consumedSignals, binding, input)
	}
	effect, err := newActionEffect(input, binding, e.state.WorldState)
	if err != nil {
		return agent.Transition{}, err
	}
	e.state.CurrentActionName = binding.action.name
	e.state.Phase = phaseAwaitingAction
	return agent.Continue(consumedSignals, effect)
}

func (e *execution) startChild(consumedSignals uint32, binding ActionBinding, input agent.Payload) (agent.Transition, error) {
	childInput := input
	if binding.childInput != nil {
		var err error
		childInput, err = binding.childInput(input, e.state.WorldState)
		if err != nil {
			return stepfail.Transition(consumedSignals, agent.FailureKindContract, failureCodePlanningChildInputFailed, err.Error())
		}
	}
	if !childInput.Valid() {
		return stepfail.Transition(
			consumedSignals, agent.FailureKindContract, failureCodePlanningChildInputInvalid,
			"Child input function returned an invalid Input",
		)
	}
	key, err := planningChildKey(binding.action.name, uint64(len(e.state.Attempts))+1)
	if err != nil {
		return agent.Transition{}, err
	}
	effect, err := agent.NewChildStartEffect(binding.childSpec(key, childInput))
	if err != nil {
		return agent.Transition{}, err
	}
	e.state.CurrentActionName = binding.action.name
	e.state.Phase = phaseAwaitingAction
	return agent.Continue(consumedSignals, effect)
}

func (e *execution) acceptAction(signals []agent.Signal) (agent.Transition, error) {
	_, failure, err := decodeSettlement[actionCompleted](signals)
	if err != nil {
		return agent.Transition{}, err
	}
	if failure != nil {
		if failure.HostError != "" {
			return stepfail.Transition(1, agent.FailureKindContract, failureCodePlanningDispatchRejected, failure.HostError)
		}
		e.state.recordFailedAction(failure.Diagnostic)
	}
	return e.requestSense(1)
}

func (e *execution) advanceChild(signals []agent.Signal) (agent.Transition, error) {
	phase := e.state.Child.Phase()
	signal, err := e.state.Child.Window(signals)
	if err != nil {
		return agent.Transition{}, fmt.Errorf("%w: %w", ErrInvalidProtocol, err)
	}
	key, err := planningChildKey(e.state.CurrentActionName, uint64(len(e.state.Attempts))+1)
	if err != nil {
		return agent.Transition{}, err
	}
	if phase == childcall.PhaseAwaitingStart {
		return e.acceptChildStart(signal, key)
	}
	waitKey, err := planningChildWaitKey(key, e.state.Child.ProcessID())
	if err != nil {
		return agent.Transition{}, err
	}
	if phase == childcall.PhaseAwaitingOpening {
		waitID, openErr := e.state.Child.AcceptOpening(signal)
		if openErr != nil {
			return agent.Transition{}, fmt.Errorf("%w: child wait opening: %w", ErrInvalidProtocol, openErr)
		}
		return agent.Wait(1, waitID)
	}
	outcome, err := e.state.Child.Complete(signal, waitKey, agent.ChildWaitBoundaryDrained)
	if err != nil {
		return agent.Transition{}, fmt.Errorf("%w: child completion: %w", ErrInvalidProtocol, err)
	}
	if !outcome.SubtreeResolved() {
		unresolved, _ := outcome.SubtreeUnresolvedEffects()
		return stepfail.Transition(1, agent.FailureKindExternal, failureCodePlanningChildUnresolvedEffects, fmt.Sprintf("child subtree %s ended with unresolved Effects %v", outcome.Result().ProcessID(), unresolved))
	}
	result := outcome.Result()
	e.state.Child = childcall.Single{}
	if result.Termination().Status() != agent.StatusCompleted {
		e.state.recordFailedAction(result.Termination().Reason())
	}
	return e.requestSense(1)
}

func (e *execution) acceptChildStart(signal agent.Signal, key agent.ChildKey) (agent.Transition, error) {
	result, err := e.state.Child.AcceptStart(signal)
	if err != nil {
		return agent.Transition{}, fmt.Errorf("%w: child start: %w", ErrInvalidProtocol, err)
	}
	if failure, failed := result.Failure(); failed {
		e.state.recordFailedAction(failure.Code() + ": " + failure.Message())
		e.state.Child = childcall.Single{}
		return e.requestSense(1)
	}
	waitKey, err := planningChildWaitKey(key, e.state.Child.ProcessID())
	if err != nil {
		return agent.Transition{}, err
	}
	effect, err := e.state.Child.WaitEffect(waitKey, agent.ChildWaitBoundaryDrained)
	if err != nil {
		return agent.Transition{}, err
	}
	return agent.Continue(1, effect)
}

func (e *execution) complete(ctx context.Context, consumedSignals uint32) (agent.Transition, error) {
	output, err := e.state.complete(ctx, e.definition)
	if err != nil {
		return agent.Transition{}, err
	}
	erased, err := agent.EncodePayload(output)
	if err != nil {
		return agent.Transition{}, err
	}
	return agent.Complete(consumedSignals, erased)
}

func planningChildKey(action string, attempt uint64) (agent.ChildKey, error) {
	return agent.ParseChildKey("planning.action." + planningIdentity(action, attempt))
}

func planningChildWaitKey(childKey agent.ChildKey, childID agent.ProcessID) (agent.WaitKey, error) {
	hash := sha256.New()
	hash.Write([]byte(childKey.String()))
	hash.Write([]byte{0})
	hash.Write([]byte(childID.String()))
	return agent.ParseWaitKey("planning.child." + hex.EncodeToString(hash.Sum(nil)))
}

func planningIdentity(action string, attempt uint64) string {
	hash := sha256.New()
	hash.Write([]byte(action))
	hash.Write([]byte{0})
	hash.Write([]byte(strconv.FormatUint(attempt, 10)))
	return hex.EncodeToString(hash.Sum(nil))
}

const (
	failureCodePlanningChildInputFailed       = "planning.child.input.failed"
	failureCodePlanningChildInputInvalid      = "planning.child.input.invalid"
	failureCodePlanningChildUnresolvedEffects = "planning.child.unresolved_effects"
	failureCodePlanningPlannerContract        = "planning.planner.contract"
	failureCodePlanningPlannerFailed          = "planning.planner.failed"
	failureCodePlanningSensingFailed          = "planning.sensing.failed"
)
