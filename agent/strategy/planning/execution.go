package planning

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strconv"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/strategy/internal/childcall"
)

type execution struct {
	definition *Definition
	state      executionState
}

// Step advances exactly one pure Planning boundary. Sensing, dispatcher
// Action I/O, and child Process work are represented as Effects and never run
// inside this method.
func (e *execution) Step(ctx context.Context, signals []agent.Signal) (agent.Transition, error) {
	if e == nil || !e.definition.valid() {
		return agent.Transition{}, ErrInvalidExecutionState
	}
	switch e.state.Phase {
	case phaseReadySense:
		if len(signals) != 0 {
			return agent.Transition{}, errors.New("planning: initial sensing does not accept Signals")
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

// Snapshot returns the complete, self-sufficient Planning state. It contains
// only Strategy-owned portable values and Framework child identities.
func (e *execution) Snapshot() (agent.ExecutionState, error) {
	if e == nil || !e.definition.valid() {
		return agent.ExecutionState{}, ErrInvalidExecutionState
	}
	return encodeExecutionState(e.state)
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

func (e *execution) acceptSense(
	ctx context.Context,
	signals []agent.Signal,
) (agent.Transition, error) {
	signal, err := oneSignal(signals)
	if err != nil {
		return agent.Transition{}, err
	}
	envelope, err := decodeSignal(signal.Payload())
	if err != nil {
		return agent.Transition{}, fmt.Errorf("%w: expected sensing Signal: %w", ErrInvalidProtocol, err)
	}
	if envelope.Operation != operationSense {
		return agent.Transition{}, fmt.Errorf("%w: expected sensing Signal", ErrInvalidProtocol)
	}
	consumedSignals := uint32(len(signals))
	if envelope.Sensing.Error != "" {
		return e.fail(
			consumedSignals, agent.FailureKindExternal, "planning.sensing.failed", envelope.Sensing.Error,
		)
	}
	e.state.WorldState = *envelope.Sensing.WorldState
	if e.state.awaitingConfirmation() {
		binding, found := e.definition.binding(e.state.CurrentActionName)
		if !found {
			return agent.Transition{}, ErrInvalidExecutionState
		}
		e.state.confirmAction(binding.action)
	}
	if e.definition.goal.SatisfiedBy(e.state.WorldState) {
		return e.complete(consumedSignals)
	}
	if e.state.attemptCount() >= uint64(e.definition.maxActionAttempts) {
		return e.complete(consumedSignals)
	}
	if e.state.PlanningPasses == math.MaxUint32 {
		return e.fail(
			consumedSignals, agent.FailureKindExecution, "planning.limit.planning_passes",
			"Planning exhausted its representable planning-pass count",
		)
	}
	problem, err := e.definition.problem(e.state)
	if err != nil {
		return e.fail(consumedSignals, agent.FailureKindContract, "planning.problem.invalid", err.Error())
	}
	plan, found, err := e.definition.planner.Plan(ctx, problem)
	if err != nil {
		return e.fail(consumedSignals, agent.FailureKindExecution, "planning.planner.failed", err.Error())
	}
	if !found {
		e.state.PlanningPasses++
		return e.complete(consumedSignals)
	}
	if err := problem.ValidatePlan(plan); err != nil {
		return e.fail(consumedSignals, agent.FailureKindContract, "planning.planner.contract", err.Error())
	}
	actions := plan.Actions()
	binding, found := e.definition.binding(actions[0].Name())
	if !found {
		return e.fail(
			consumedSignals, agent.FailureKindContract, "planning.planner.contract",
			"Planner selected an Action outside the Planning Definition",
		)
	}
	return e.startAction(consumedSignals, binding)
}

func (e *execution) startAction(
	consumedSignals uint32,
	binding ActionBinding,
) (agent.Transition, error) {
	input, err := e.state.input()
	if err != nil {
		return agent.Transition{}, err
	}
	switch binding.target {
	case bindingTargetDispatcher:
		effect, newActionEffectErr := newActionEffect(input, binding, e.state.WorldState)
		if newActionEffectErr != nil {
			return agent.Transition{}, newActionEffectErr
		}
		e.state.PlanningPasses++
		e.state.CurrentActionName = binding.action.name
		e.state.Phase = phaseAwaitingAction
		return agent.Continue(consumedSignals, effect)
	case bindingTargetChild:
		childInput := input
		if binding.childInput != nil {
			childInput, err = binding.childInput(input, e.state.WorldState)
			if err != nil {
				return e.fail(
					consumedSignals, agent.FailureKindContract, "planning.child.input.failed", err.Error(),
				)
			}
		}
		if !childInput.Valid() {
			return e.fail(
				consumedSignals, agent.FailureKindContract, "planning.child.input.invalid",
				"Child input function returned an invalid Input",
			)
		}
		key, err := planningChildKey(binding.action.name, uint32(len(e.state.Attempts)+1))
		if err != nil {
			return agent.Transition{}, err
		}
		effect, err := agent.StartChild(binding.childSpec(key, childInput))
		if err != nil {
			return agent.Transition{}, err
		}
		e.state.PlanningPasses++
		e.state.CurrentActionName = binding.action.name
		e.state.Child = &childcall.Single{}
		e.state.Phase = phaseChild
		return agent.Continue(consumedSignals, effect)
	default:
		return agent.Transition{}, ErrInvalidAction
	}
}

func (e *execution) acceptAction(signals []agent.Signal) (agent.Transition, error) {
	signal, err := oneSignal(signals)
	if err != nil {
		return agent.Transition{}, err
	}
	envelope, err := decodeSignal(signal.Payload())
	if err != nil {
		return agent.Transition{}, fmt.Errorf("%w: expected Action Signal: %w", ErrInvalidProtocol, err)
	}
	if envelope.Operation != operationAction {
		return agent.Transition{}, fmt.Errorf("%w: expected Action Signal", ErrInvalidProtocol)
	}
	consumedSignals := uint32(len(signals))
	if !envelope.Action.Succeeded {
		e.state.recordFailedAction(envelope.Action.Diagnostic)
	}
	return e.requestSense(consumedSignals)
}

func (e *execution) advanceChild(signals []agent.Signal) (agent.Transition, error) {
	phase := e.state.Child.Phase()
	if len(signals) == 0 || phase != childcall.AwaitingOpening && len(signals) != 1 {
		return agent.Transition{}, fmt.Errorf("%w: child handshake requires its settlement Signal", ErrInvalidProtocol)
	}
	key, err := planningChildKey(e.state.CurrentActionName, uint32(len(e.state.Attempts)+1))
	if err != nil {
		return agent.Transition{}, err
	}
	if phase == childcall.AwaitingStart {
		return e.acceptChildStart(signals[0], key)
	}
	waitKey, err := planningChildWaitKey(key, e.state.Child.ProcessID())
	if err != nil {
		return agent.Transition{}, err
	}
	if phase == childcall.AwaitingOpening {
		waitID, openErr := e.state.Child.AcceptOpening(signals[0], waitKey, agent.ChildWaitBoundaryDrained)
		if openErr != nil {
			return agent.Transition{}, fmt.Errorf("%w: child wait opening: %w", ErrInvalidProtocol, openErr)
		}
		return agent.Wait(1, waitID)
	}
	result, err := e.state.Child.Complete(signals[0], key, waitKey, agent.ChildWaitBoundaryDrained)
	if err != nil {
		return agent.Transition{}, fmt.Errorf("%w: child completion: %w", ErrInvalidProtocol, err)
	}
	e.state.Child = nil
	if result.Status() != agent.StatusCompleted {
		e.state.recordFailedAction(result.Termination().Reason())
	}
	return e.requestSense(1)
}

func (e *execution) acceptChildStart(signal agent.Signal, key agent.ChildKey) (agent.Transition, error) {
	binding, found := e.definition.binding(e.state.CurrentActionName)
	if !found || binding.target != bindingTargetChild {
		return agent.Transition{}, ErrInvalidExecutionState
	}
	result, err := e.state.Child.AcceptStart(signal, key, binding.child.DeploymentRef)
	if err != nil {
		return agent.Transition{}, fmt.Errorf("%w: child start: %w", ErrInvalidProtocol, err)
	}
	if failure, failed := result.Failure(); failed {
		e.state.recordFailedAction(failure.Code() + ": " + failure.Message())
		e.state.Child = nil
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

func (e *execution) complete(consumedSignals uint32) (agent.Transition, error) {
	output, err := e.state.complete(e.definition)
	if err != nil {
		return agent.Transition{}, err
	}
	erased, err := agent.EncodeOutput(output)
	if err != nil {
		return agent.Transition{}, err
	}
	return agent.Complete(consumedSignals, erased)
}

func (e *execution) fail(
	consumedSignals uint32,
	kind agent.FailureKind,
	code string,
	message string,
) (agent.Transition, error) {
	failure, err := agent.NewFailure(kind, code, diagnostic(message))
	if err != nil {
		return agent.Transition{}, err
	}
	return agent.Fail(consumedSignals, failure)
}

func planningChildKey(action string, attempt uint32) (agent.ChildKey, error) {
	return agent.ParseChildKey("planning.action." + planningIdentity(action, attempt))
}

func planningChildWaitKey(childKey agent.ChildKey, childID agent.ProcessID) (agent.WaitKey, error) {
	hash := sha256.New()
	hash.Write([]byte(childKey.String()))
	hash.Write([]byte{0})
	hash.Write([]byte(childID.String()))
	return agent.ParseWaitKey("planning.child." + hex.EncodeToString(hash.Sum(nil)))
}

func planningIdentity(action string, attempt uint32) string {
	hash := sha256.New()
	hash.Write([]byte(action))
	hash.Write([]byte{0})
	hash.Write([]byte(strconv.FormatUint(uint64(attempt), 10)))
	return hex.EncodeToString(hash.Sum(nil))
}
