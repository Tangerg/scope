package interaction

import (
	"fmt"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/core/chat"
)

func (e *execution) childBindings(calls []chat.ToolCall) ([]agent.DeploymentRef, error) {
	bindings := make([]agent.DeploymentRef, len(calls))
	for index, call := range calls {
		if e.state.ChildBatch.Kind == childCallsTool {
			bindings[index] = e.definition.tools.deploymentRef
			continue
		}
		delegate, found := e.definition.delegate(call.Name)
		if !found {
			return nil, ErrInvalidExecutionState
		}
		bindings[index] = delegate.deploymentRef
	}
	return bindings, nil
}

func (e *execution) acceptChildStarts(signals []agent.Signal) (agent.Transition, error) {
	starts, steer, consumed, err := collectChildStarts(signals)
	if err != nil {
		return agent.Transition{}, err
	}
	if steerErr := e.addSteer(steer); steerErr != nil {
		return agent.Transition{}, steerErr
	}
	calls, err := e.activeCallSegment()
	if err != nil {
		return agent.Transition{}, err
	}
	bindings, err := e.childBindings(calls)
	if err != nil {
		return agent.Transition{}, err
	}
	batch := e.state.ChildBatch
	indices, err := batch.acceptStarts(starts, bindings)
	if err != nil {
		return agent.Transition{}, err
	}
	for offset, index := range indices {
		failure, failed := starts[offset].Failure()
		if !failed {
			continue
		}
		if batch.Kind == childCallsTool {
			return e.fail(consumed, agent.FailureKindExecution, "interaction.tool.start_failed", failure.Message())
		}
		result := delegateErrorResult(calls[index], "child start failed: "+failure.Code()+": "+failure.Message())
		batch.Invocations[index].Result = &toolCallResult{Result: &result}
	}
	if len(batch.children()) == 0 {
		if err := e.finishChildBatch(); err != nil {
			return agent.Transition{}, err
		}
		return e.advanceToolCallBatch(consumed)
	}
	return e.waitForChildren(consumed)
}

func (e *execution) waitForChildren(consumed uint32) (agent.Transition, error) {
	spec, err := e.state.ChildBatch.waitSpec(e.state.ModelCallCount, e.state.nextToolCallIndex())
	if err != nil {
		return agent.Transition{}, err
	}
	effect, err := agent.WaitForChildren(spec)
	if err != nil {
		return agent.Transition{}, err
	}
	e.state.Phase = phaseAwaitingChildWaitOpen
	return agent.Continue(consumed, effect)
}

func (e *execution) acceptChildWaitOpen(signals []agent.Signal) (agent.Transition, error) {
	opened, steer, consumed, err := collectChildWaitOpened(signals)
	if err != nil {
		return agent.Transition{}, err
	}
	if steerErr := e.addSteer(steer); steerErr != nil {
		return agent.Transition{}, steerErr
	}
	want, err := e.state.ChildBatch.waitSpec(e.state.ModelCallCount, e.state.nextToolCallIndex())
	if err != nil {
		return agent.Transition{}, err
	}
	if err := e.state.ChildBatch.acceptWaitOpened(opened, want); err != nil {
		return agent.Transition{}, err
	}
	e.state.Phase = phaseWaitingChildren
	return agent.Wait(consumed, opened.WaitID())
}

func (e *execution) acceptChildCompletions(signals []agent.Signal) (agent.Transition, error) {
	completed, steer, consumed, err := collectChildWaitSatisfied(signals)
	if err != nil {
		return agent.Transition{}, err
	}
	if steerErr := e.addSteer(steer); steerErr != nil {
		return agent.Transition{}, steerErr
	}
	calls, err := e.activeCallSegment()
	if err != nil {
		return agent.Transition{}, err
	}
	batch := e.state.ChildBatch
	want, err := batch.waitSpec(e.state.ModelCallCount, e.state.nextToolCallIndex())
	if err != nil {
		return agent.Transition{}, err
	}
	indices, err := batch.validateCompletions(completed, want)
	if err != nil {
		return agent.Transition{}, err
	}
	for offset, outcome := range completed.Outcomes() {
		index := indices[offset]
		result := outcome.Result()
		if batch.Kind == childCallsDelegate {
			if err := e.acceptDelegateOutcome(index, calls[index], result); err != nil {
				return agent.Transition{}, err
			}
			continue
		}
		if result.Status() != agent.StatusCompleted {
			return e.fail(consumed, agent.FailureKindExecution, "interaction.tool.process_failed", "Tool child ended with "+result.Status().String())
		}
		encoded, present := result.Output()
		if !present {
			return agent.Transition{}, ErrInvalidExecutionState
		}
		decoded, err := encoded.Decode[toolCallResult]()
		if err != nil {
			return agent.Transition{}, err
		}
		if err := decoded.validateCall(calls[index]); err != nil {
			return agent.Transition{}, fmt.Errorf("%w: %w", ErrInvalidExecutionState, err)
		}
		batch.Invocations[index].Result = &decoded
	}
	batch.WaitID = nil
	if batch.Kind == childCallsTool {
		return e.scheduleToolChildren(consumed)
	}
	if err := e.finishChildBatch(); err != nil {
		return agent.Transition{}, err
	}
	return e.advanceToolCallBatch(consumed)
}

func (e *execution) finishChildBatch() error {
	for _, invocation := range e.state.ChildBatch.Invocations {
		if invocation.Result == nil || invocation.Result.Result == nil {
			return ErrInvalidExecutionState
		}
		e.state.SettledToolResults = append(e.state.SettledToolResults, invocation.Result.Result.Clone())
		names, err := mergeAdvertisedToolNames(e.state.AdvertisedToolNames, invocation.Result.AdvertisedToolNames)
		if err != nil {
			return err
		}
		e.state.AdvertisedToolNames = names
		e.state.DirectToolResultEligible = e.state.DirectToolResultEligible && invocation.Result.Direct
	}
	e.state.ChildBatch = nil
	return nil
}
