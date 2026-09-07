package planning

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"

	agent "github.com/Tangerg/scope/agent"
)

type execution struct {
	definition *Definition
	state      executionState
}

// Step advances exactly one pure Planning boundary. Observation, dispatcher
// Action I/O, and child Process work are represented as Effects and never run
// inside this method.
func (e *execution) Step(ctx context.Context, signals []agent.Signal) (agent.Transition, error) {
	if e == nil || !e.definition.valid() {
		return agent.Transition{}, ErrInvalidExecutionState
	}
	if err := e.state.validate(e.definition); err != nil {
		return agent.Transition{}, err
	}
	switch e.state.Phase {
	case phaseReadyObservation:
		if len(signals) != 0 {
			return agent.Transition{}, errors.New("planning: initial observation does not accept Signals")
		}
		return e.requestObservation(0)
	case phaseAwaitingObservation:
		return e.acceptObservation(ctx, signals)
	case phaseAwaitingAction:
		return e.acceptAction(signals)
	case phaseAwaitingChildStart:
		return e.acceptChildStart(signals)
	case phaseAwaitingChildWaitOpen:
		return e.acceptChildWaitOpen(signals)
	case phaseWaitingChild:
		return e.acceptChildCompletion(signals)
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
	if err := e.state.validate(e.definition); err != nil {
		return agent.ExecutionState{}, err
	}
	return encodeExecutionState(e.state)
}

func (e *execution) requestObservation(consumedSignals uint32) (agent.Transition, error) {
	input, err := e.state.input()
	if err != nil {
		return agent.Transition{}, err
	}
	effect, err := newObservationEffect(input)
	if err != nil {
		return agent.Transition{}, err
	}
	e.state.Phase = phaseAwaitingObservation
	return agent.Continue(consumedSignals, effect)
}

func (e *execution) acceptObservation(
	ctx context.Context,
	signals []agent.Signal,
) (agent.Transition, error) {
	signal, err := oneSignal(signals)
	if err != nil {
		return agent.Transition{}, err
	}
	envelope, err := decodeSignal(signal.Payload())
	if err != nil || envelope.Operation != operationObserve {
		return agent.Transition{}, fmt.Errorf("%w: expected observation Signal", ErrInvalidProtocol)
	}
	consumedSignals := uint32(len(signals))
	if envelope.Observation.Error != "" {
		return e.fail(
			consumedSignals, agent.FailureKindExternal, "planning.observation.failed", envelope.Observation.Error,
		)
	}
	e.state.WorldState = *envelope.Observation.WorldState
	if e.state.ActionConfirmationPending {
		if confirmActionErr := e.confirmAction(); confirmActionErr != nil {
			return agent.Transition{}, confirmActionErr
		}
	}
	if e.definition.goal.SatisfiedBy(e.state.WorldState) {
		return e.complete(consumedSignals, OutcomeAchieved)
	}
	if uint64(len(e.state.Attempts)) >= uint64(e.definition.maxActionAttempts) {
		return e.complete(consumedSignals, OutcomeStuck)
	}
	if e.state.PlanningPasses == math.MaxUint32 {
		return e.fail(
			consumedSignals, agent.FailureKindExecution, "planning.limit.planning_passes",
			"Planning exhausted its representable planning-pass count",
		)
	}
	problem, err := e.definition.problem(e.state.WorldState, e.state.ExcludedActionNames)
	if err != nil {
		return e.fail(consumedSignals, agent.FailureKindContract, "planning.problem.invalid", err.Error())
	}
	plan, found, err := e.definition.planner.Plan(ctx, problem)
	if err != nil {
		return e.fail(consumedSignals, agent.FailureKindExecution, "planning.planner.failed", err.Error())
	}
	if !found {
		e.state.PlanningPasses++
		outcome := OutcomeUnreachable
		if len(e.state.Attempts) > 0 {
			outcome = OutcomeStuck
		}
		return e.complete(consumedSignals, outcome)
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
		e.state.ChildKey = &key
		e.state.Phase = phaseAwaitingChildStart
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
	if err != nil || envelope.Operation != operationAction {
		return agent.Transition{}, fmt.Errorf("%w: expected Action Signal", ErrInvalidProtocol)
	}
	consumedSignals := uint32(len(signals))
	if envelope.Action.Succeeded {
		e.state.ActionConfirmationPending = true
		return e.requestObservation(consumedSignals)
	}
	e.recordFailedAction(envelope.Action.Diagnostic)
	return e.requestObservation(consumedSignals)
}

func (e *execution) acceptChildStart(signals []agent.Signal) (agent.Transition, error) {
	if len(signals) != 1 {
		return agent.Transition{}, errors.New("planning: child start requires exactly one settlement Signal")
	}
	result, err := agent.ParseChildStartResult(signals[0])
	binding, found := e.definition.binding(e.state.CurrentActionName)
	if err != nil || !found || binding.target != bindingTargetChild || e.state.ChildKey == nil ||
		result.Key() != *e.state.ChildKey || result.DeploymentRef() != binding.child.DeploymentRef {
		return agent.Transition{}, fmt.Errorf("%w: child-start result mismatch", ErrInvalidProtocol)
	}
	consumedSignals := uint32(len(signals))
	if failure, failed := result.Failure(); failed {
		e.recordFailedAction(failure.Code() + ": " + failure.Message())
		e.clearChild()
		return e.requestObservation(consumedSignals)
	}
	childID, started := result.ProcessID()
	if !started {
		return agent.Transition{}, fmt.Errorf("%w: child-start result has no Process", ErrInvalidProtocol)
	}
	waitKey, err := planningChildWaitKey(*e.state.ChildKey, childID)
	if err != nil {
		return agent.Transition{}, err
	}
	effect, err := agent.WaitForChildren(agent.ChildWaitSpec{
		Key: waitKey, Children: []agent.ProcessID{childID}, Condition: agent.AllChildren(),
	})
	if err != nil {
		return agent.Transition{}, err
	}
	e.state.ChildProcessID = &childID
	e.state.Phase = phaseAwaitingChildWaitOpen
	return agent.Continue(consumedSignals, effect)
}

func (e *execution) acceptChildWaitOpen(signals []agent.Signal) (agent.Transition, error) {
	if len(signals) == 0 || e.state.ChildKey == nil || e.state.ChildProcessID == nil {
		return agent.Transition{}, errors.New("planning: child wait opening requires its settlement Signal")
	}
	opened, err := agent.ParseChildWaitOpened(signals[0])
	if err != nil {
		return agent.Transition{}, fmt.Errorf("%w: child wait opening: %w", ErrInvalidProtocol, err)
	}
	wantKey, err := planningChildWaitKey(*e.state.ChildKey, *e.state.ChildProcessID)
	spec := opened.Spec()
	if err != nil || spec.Key != wantKey || len(spec.Children) != 1 ||
		spec.Children[0] != *e.state.ChildProcessID || spec.Condition != agent.AllChildren() {
		return agent.Transition{}, fmt.Errorf("%w: child wait opening mismatch", ErrInvalidProtocol)
	}
	waitID := opened.WaitID()
	e.state.WaitID = &waitID
	e.state.Phase = phaseWaitingChild
	return agent.Wait(1, waitID)
}

func (e *execution) acceptChildCompletion(signals []agent.Signal) (agent.Transition, error) {
	if len(signals) != 1 || e.state.ChildKey == nil || e.state.ChildProcessID == nil || e.state.WaitID == nil {
		return agent.Transition{}, errors.New("planning: child completion requires one active child wait Signal")
	}
	completed, err := agent.ParseChildrenCompleted(signals[0])
	if err != nil || completed.WaitID() != *e.state.WaitID {
		return agent.Transition{}, fmt.Errorf("%w: child completion mismatch", ErrInvalidProtocol)
	}
	wantWaitKey, err := planningChildWaitKey(*e.state.ChildKey, *e.state.ChildProcessID)
	if err != nil || completed.Key() != wantWaitKey {
		return agent.Transition{}, fmt.Errorf("%w: child completion wait key mismatch", ErrInvalidProtocol)
	}
	outcomes := completed.Outcomes()
	if len(outcomes) != 1 || outcomes[0].Key() != *e.state.ChildKey ||
		outcomes[0].Result().ProcessID() != *e.state.ChildProcessID {
		return agent.Transition{}, fmt.Errorf("%w: child completion outcome mismatch", ErrInvalidProtocol)
	}
	result := outcomes[0].Result()
	e.clearChild()
	if result.Status() == agent.StatusCompleted {
		e.state.ActionConfirmationPending = true
	} else {
		e.recordFailedAction(result.Termination().Reason())
	}
	return e.requestObservation(uint32(len(signals)))
}

func (e *execution) confirmAction() error {
	binding, found := e.definition.binding(e.state.CurrentActionName)
	if !found {
		return ErrInvalidExecutionState
	}
	if e.state.WorldState.Satisfies(binding.action.effects...) {
		e.state.Attempts = append(e.state.Attempts, Attempt{
			ActionName: e.state.CurrentActionName, Status: AttemptSucceeded,
		})
	} else {
		e.state.Attempts = append(e.state.Attempts, Attempt{
			ActionName: e.state.CurrentActionName, Status: AttemptUnconfirmed,
			Diagnostic: "Reobservation did not establish the Action's predicted effects",
		})
		e.state.excludeAction(e.state.CurrentActionName)
	}
	e.state.CurrentActionName = ""
	e.state.ActionConfirmationPending = false
	return nil
}

func (e *execution) recordFailedAction(reason string) {
	e.state.Attempts = append(e.state.Attempts, Attempt{
		ActionName: e.state.CurrentActionName, Status: AttemptFailed, Diagnostic: diagnostic(reason),
	})
	e.state.excludeAction(e.state.CurrentActionName)
	e.state.CurrentActionName = ""
	e.state.ActionConfirmationPending = false
}

func (e *execution) clearChild() {
	e.state.ChildKey = nil
	e.state.ChildProcessID = nil
	e.state.WaitID = nil
}

func (e *execution) complete(consumedSignals uint32, outcome Outcome) (agent.Transition, error) {
	attempts := slices.Clone(e.state.Attempts)
	if attempts == nil {
		attempts = []Attempt{}
	}
	output := Output{
		Outcome: outcome, WorldState: e.state.WorldState,
		Attempts: attempts, PlanningPasses: e.state.PlanningPasses,
	}
	if err := output.Validate(); err != nil {
		return agent.Transition{}, err
	}
	if outcome == OutcomeAchieved && !e.definition.goal.SatisfiedBy(output.WorldState) {
		return agent.Transition{}, ErrInvalidExecutionState
	}
	erased, err := agent.EncodeOutput(output)
	if err != nil {
		return agent.Transition{}, err
	}
	e.state.Phase = phaseCompleted
	e.state.CurrentActionName = ""
	e.state.ActionConfirmationPending = false
	e.clearChild()
	e.state.FinalOutput = &output
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
