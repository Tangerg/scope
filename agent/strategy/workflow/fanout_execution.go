package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/strategy/internal/childcall"
)

func (e *execution) startFanoutWindow(ctx context.Context, consumedSignals uint32) (agent.Transition, error) {
	stage := e.stage()
	start := e.state.fanoutWindowStart()
	inputs, count, err := stage.fanoutWindowInputs(start, e.state.CurrentValue)
	if err != nil {
		if _, exceeded := errors.AsType[mapMaxItemsExceededError](err); exceeded {
			return e.failContract(consumedSignals, stage.failureCode("max_items_exceeded"),
				"Map Stage "+stage.id+" input exceeds its configured maximum items")
		}
		return agent.Transition{}, err
	}
	if start > count || stage.fanoutWindowSize() == 0 ||
		uint32(len(inputs)) != min(stage.fanoutWindowSize(), count-start) {
		return agent.Transition{}, ErrInvalidExecutionState
	}
	if start == count {
		value, err := stage.fanoutComplete(ctx, e.state.CompletedFanoutOutputs)
		if err != nil {
			return agent.Transition{}, err
		}
		e.state.CurrentValue = value
		e.state.Phase = phaseReady
		return e.finishStage(consumedSignals)
	}
	end := start + uint32(len(inputs))
	window := make([]fanoutChildState, end-start)
	effects := make([]agent.Effect, 0, end-start)
	for index := start; index < end; index++ {
		binding, found := stage.fanoutBinding(index)
		if !found {
			return agent.Transition{}, ErrInvalidStage
		}
		input := inputs[index-start]
		key, err := e.fanoutChildKey(index)
		if err != nil {
			return agent.Transition{}, err
		}
		effect, err := agent.StartChild(agent.ChildSpec{
			Key: key, DeploymentRef: binding.deploymentRef, Input: input,
			Budget: binding.budget, Capabilities: binding.capabilities,
		})
		if err != nil {
			return agent.Transition{}, err
		}
		effects = append(effects, effect)
	}
	e.state.ActiveFanoutWindow = window
	e.state.Phase = phaseAwaitingFanoutStarts
	return agent.Continue(consumedSignals, effects...)
}

func (e *execution) acceptFanoutStarts(signals []agent.Signal) (agent.Transition, error) {
	window := e.state.ActiveFanoutWindow
	if len(signals) < len(window) {
		return agent.Transition{}, fmt.Errorf("%w: fan-out child starts require one settlement Signal per member", ErrInvalidProtocol)
	}
	childIDs := make([]agent.ProcessID, 0, len(window))
	for offset := range window {
		index := e.state.fanoutWindowStart() + uint32(offset)
		binding, found := e.stage().fanoutBinding(index)
		memberID, identified := e.stage().fanoutMemberID(index)
		result, err := agent.ParseChildStartResult(signals[offset])
		key, keyErr := e.fanoutChildKey(index)
		if err != nil {
			return agent.Transition{}, fmt.Errorf("%w: fan-out child start: %w", ErrInvalidProtocol, err)
		}
		if keyErr != nil {
			return agent.Transition{}, fmt.Errorf("%w: fan-out child key: %w", ErrInvalidProtocol, keyErr)
		}
		if !found || !identified ||
			!childcall.StartMatches(result, key, binding.deploymentRef) {
			return agent.Transition{}, fmt.Errorf(
				"%w: %s Stage %q member %q start result mismatch",
				ErrInvalidProtocol, e.stage().kind, e.stage().id, memberID,
			)
		}
		if failure, failed := result.Failure(); failed {
			e.state.ActiveFanoutWindow[offset].Failure = &failure
			continue
		}
		processID, started := result.ProcessID()
		if !started {
			return agent.Transition{}, fmt.Errorf("%w: fan-out child-start result has no Process", ErrInvalidProtocol)
		}
		e.state.ActiveFanoutWindow[offset].ChildProcessID = &processID
		childIDs = append(childIDs, processID)
	}
	consumedSignals := uint32(len(window))
	if len(childIDs) == 0 {
		return agent.Fail(consumedSignals, e.firstFanoutFailure())
	}
	waitKey, err := e.fanoutWaitKey()
	if err != nil {
		return agent.Transition{}, err
	}
	effect, err := agent.WaitForChildren(agent.ChildWaitSpec{
		Boundary: agent.ChildWaitBoundaryDrained,
		Key:      waitKey, Children: childIDs, Condition: agent.AllChildren(),
	})
	if err != nil {
		return agent.Transition{}, err
	}
	e.state.Phase = phaseAwaitingFanoutWaitOpen
	return agent.Continue(consumedSignals, effect)
}

func (e *execution) acceptFanoutWaitOpen(signals []agent.Signal) (agent.Transition, error) {
	if len(signals) == 0 {
		return agent.Transition{}, fmt.Errorf("%w: fan-out wait opening requires its settlement Signal", ErrInvalidProtocol)
	}
	opened, err := agent.ParseChildWaitOpened(signals[0])
	wantKey, keyErr := e.fanoutWaitKey()
	wantChildren := e.fanoutStartedChildren()
	if err != nil {
		return agent.Transition{}, fmt.Errorf("%w: fan-out child wait does not match Stage %q: %w", ErrInvalidProtocol, e.stage().id, err)
	}
	if keyErr != nil {
		return agent.Transition{}, fmt.Errorf("%w: fan-out child wait does not match Stage %q: %w", ErrInvalidProtocol, e.stage().id, keyErr)
	}
	if !childcall.OpeningMatches(opened, agent.ChildWaitSpec{
		Key: wantKey, Boundary: agent.ChildWaitBoundaryDrained,
		Children: wantChildren, Condition: agent.AllChildren(),
	}) {
		return agent.Transition{}, fmt.Errorf("%w: fan-out child wait does not match Stage %q", ErrInvalidProtocol, e.stage().id)
	}
	waitID := opened.WaitID()
	e.state.FanoutWaitID = &waitID
	e.state.Phase = phaseWaitingFanout
	return agent.Wait(1, waitID)
}

func (e *execution) acceptFanoutCompletion(ctx context.Context, signals []agent.Signal) (agent.Transition, error) {
	if len(signals) == 0 || e.state.FanoutWaitID == nil {
		return agent.Transition{}, fmt.Errorf("%w: fan-out completion requires one active child wait Signal", ErrInvalidProtocol)
	}
	completed, err := agent.ParseChildWaitSatisfied(signals[0])
	wantKey, keyErr := e.fanoutWaitKey()
	if err != nil {
		return agent.Transition{}, fmt.Errorf("%w: fan-out completion does not match Stage %q: %w", ErrInvalidProtocol, e.stage().id, err)
	}
	if keyErr != nil {
		return agent.Transition{}, fmt.Errorf("%w: fan-out completion does not match Stage %q: %w", ErrInvalidProtocol, e.stage().id, keyErr)
	}
	if !childcall.CompletionMatches(completed, *e.state.FanoutWaitID, wantKey, agent.ChildWaitBoundaryDrained) {
		return agent.Transition{}, fmt.Errorf("%w: fan-out completion does not match Stage %q", ErrInvalidProtocol, e.stage().id)
	}
	outcomes := completed.Outcomes()
	if len(outcomes) != len(e.fanoutStartedChildren()) {
		return agent.Transition{}, fmt.Errorf("%w: fan-out completion outcome count mismatch", ErrInvalidProtocol)
	}
	windowOutputs := make([]json.RawMessage, len(e.state.ActiveFanoutWindow))
	outcomeIndex := 0
	for offset := range e.state.ActiveFanoutWindow {
		child := &e.state.ActiveFanoutWindow[offset]
		if child.ChildProcessID == nil {
			continue
		}
		outcome := outcomes[outcomeIndex]
		outcomeIndex++
		index := e.state.fanoutWindowStart() + uint32(offset)
		wantChildKey, fanoutChildKeyErr := e.fanoutChildKey(index)
		if fanoutChildKeyErr != nil {
			return agent.Transition{}, fmt.Errorf("%w: fan-out member outcome mismatch: %w", ErrInvalidProtocol, fanoutChildKeyErr)
		}
		if !childcall.OutcomeMatches(outcome, wantChildKey, *child.ChildProcessID) {
			return agent.Transition{}, fmt.Errorf("%w: fan-out member outcome mismatch", ErrInvalidProtocol)
		}
		failure, output, outcomeErr := e.fanoutOutcome(index, outcome.Result())
		if outcomeErr != nil {
			return agent.Transition{}, outcomeErr
		}
		if failure != nil {
			child.Failure = failure
			continue
		}
		windowOutputs[offset] = output
	}
	if failure := e.firstFanoutFailure(); failure.Valid() {
		return agent.Fail(1, failure)
	}
	e.state.CompletedFanoutOutputs = append(e.state.CompletedFanoutOutputs, windowOutputs...)
	e.state.FanoutWaitID = nil
	e.state.ActiveFanoutWindow = nil
	return e.startFanoutWindow(ctx, 1)
}

func (e *execution) fanoutOutcome(
	index uint32,
	result agent.Result,
) (*agent.Failure, json.RawMessage, error) {
	if result.Status() != agent.StatusCompleted {
		if failure, failed := result.Termination().Failure(); failed {
			return &failure, nil, nil
		}
		code := e.stage().fanoutFailureCode("not_completed")
		message := e.fanoutFailureMessage(index, "terminated with status "+result.Status().String())
		failure, err := agent.NewFailure(agent.FailureKindExternal, code, message)
		return &failure, nil, err
	}
	output, present := result.Output()
	if !present {
		failure, err := agent.NewFailure(
			agent.FailureKindContract, e.stage().fanoutFailureCode("output_missing"),
			e.fanoutFailureMessage(index, "returned no Output"),
		)
		return &failure, nil, err
	}
	if err := e.stage().fanoutOutputSchema().ValidateOutput(output); err != nil {
		failure, failureErr := agent.NewFailure(
			agent.FailureKindContract, e.stage().fanoutFailureCode("output_invalid"),
			e.fanoutFailureMessage(index, "violated its Output contract"),
		)
		return &failure, nil, failureErr
	}
	return nil, output.JSON(), nil
}

func (e *execution) fanoutFailureMessage(index uint32, diagnostic string) string {
	return string(e.stage().kind) + " Stage " + e.stage().id + " " +
		e.stage().fanoutMemberLabel(index) + " " + diagnostic
}

func (e *execution) firstFanoutFailure() agent.Failure {
	for _, child := range e.state.ActiveFanoutWindow {
		if child.Failure != nil && child.Failure.Valid() {
			return *child.Failure
		}
	}
	return agent.Failure{}
}

func (e *execution) fanoutStartedChildren() []agent.ProcessID {
	children := make([]agent.ProcessID, 0, len(e.state.ActiveFanoutWindow))
	for _, child := range e.state.ActiveFanoutWindow {
		if child.ChildProcessID != nil {
			children = append(children, *child.ChildProcessID)
		}
	}
	return children
}

func (e *execution) fanoutChildKey(index uint32) (agent.ChildKey, error) {
	memberID, found := e.stage().fanoutMemberID(index)
	if !found {
		return agent.ChildKey{}, ErrInvalidExecutionState
	}
	return workflowChildKey(string(e.stage().kind), e.stage().id, memberID)
}

func (e *execution) fanoutWaitKey() (agent.WaitKey, error) {
	return workflowWaitKey(
		string(e.stage().kind), e.stage().id,
		strconv.FormatUint(uint64(e.state.fanoutWindowStart()), 10),
	)
}

func (e *execution) clearFanout() {
	e.state.ActiveFanoutWindow = nil
	e.state.CompletedFanoutOutputs = nil
}
