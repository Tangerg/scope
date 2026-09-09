package interaction

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strconv"
	"strings"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/core/chat"
)

func (e *execution) startDelegateSegment(
	consumedSignals uint32,
	calls []chat.ToolCall,
) (agent.Transition, bool, error) {
	start := e.state.nextToolCallIndex()
	end := start
	for end < uint32(len(calls)) {
		if _, delegated := e.definition.delegate(calls[end].Name); !delegated {
			break
		}
		end++
	}
	segment := delegateSegmentState{Invocations: make([]delegateInvocationState, end-start)}
	effects := make([]agent.Effect, 0, len(segment.Invocations))
	for offset := range segment.Invocations {
		call := calls[start+uint32(offset)]
		delegate, _ := e.definition.delegate(call.Name)
		arguments := strings.TrimSpace(call.Arguments)
		if arguments == "" {
			arguments = "{}"
		}
		input, err := agent.ParseInput([]byte(arguments))
		if err != nil {
			result := delegateErrorResult(call, "arguments are not valid JSON: "+err.Error())
			segment.Invocations[offset].ToolResult = &result
			continue
		}
		if validateInputErr := delegate.validateInput(input); validateInputErr != nil {
			result := delegateErrorResult(call, "arguments violate the delegated worker input contract: "+validateInputErr.Error())
			segment.Invocations[offset].ToolResult = &result
			continue
		}
		key, err := DelegateChildKey(e.state.ModelCallCount, call)
		if err != nil {
			return agent.Transition{}, false, err
		}
		effect, err := agent.StartChild(agent.ChildSpec{
			Key: key, DeploymentRef: delegate.deploymentRef, Input: input,
			Budget: delegate.budget, Capabilities: delegate.capabilities,
		})
		if err != nil {
			return agent.Transition{}, false, err
		}
		segment.Invocations[offset].ChildKey = &key
		effects = append(effects, effect)
	}
	e.state.ActiveToolCallEndIndex = end
	e.state.DelegateSegment = &segment
	if len(effects) == 0 {
		results, err := delegateSegmentResults(segment)
		if err != nil {
			return agent.Transition{}, false, err
		}
		e.state.SettledToolResults = append(e.state.SettledToolResults, results...)
		e.state.ActiveToolCallEndIndex = 0
		e.state.DelegateSegment = nil
		return agent.Transition{}, false, nil
	}
	e.state.Phase = phaseAwaitingDelegateStarts
	transition, err := agent.Continue(consumedSignals, effects...)
	return transition, true, err
}

func (e *execution) acceptDelegateStarts(signals []agent.Signal) (agent.Transition, error) {
	starts, steer, consumedSignals, err := collectChildStarts(signals)
	if err != nil {
		return agent.Transition{}, err
	}
	if addSteerErr := e.addSteer(steer); addSteerErr != nil {
		return agent.Transition{}, addSteerErr
	}
	calls, err := e.activeCallSegment()
	if err != nil || e.state.DelegateSegment == nil {
		return agent.Transition{}, ErrInvalidExecutionState
	}
	next := 0
	for index := range e.state.DelegateSegment.Invocations {
		invocation := &e.state.DelegateSegment.Invocations[index]
		if invocation.ChildKey == nil || invocation.ToolResult != nil {
			continue
		}
		if next >= len(starts) {
			return agent.Transition{}, fmt.Errorf("%w: missing Delegate child-start result", ErrInvalidExecutionState)
		}
		start := starts[next]
		next++
		delegate, _ := e.definition.delegate(calls[index].Name)
		if start.Key() != *invocation.ChildKey || start.DeploymentRef() != delegate.deploymentRef {
			return agent.Transition{}, fmt.Errorf("%w: Delegate child-start result mismatch", ErrInvalidExecutionState)
		}
		if failure, failed := start.Failure(); failed {
			result := delegateErrorResult(
				calls[index], "child start failed: "+failure.Code()+": "+failure.Message(),
			)
			invocation.ToolResult = &result
			continue
		}
		processID, started := start.ProcessID()
		if !started {
			return agent.Transition{}, fmt.Errorf("%w: Delegate child start has no outcome", ErrInvalidExecutionState)
		}
		invocation.ChildProcessID = &processID
	}
	if next != len(starts) {
		return agent.Transition{}, fmt.Errorf("%w: unexpected Delegate child-start result", ErrInvalidExecutionState)
	}
	children := e.delegateChildren()
	if len(children) == 0 {
		results, delegateSegmentResultsErr := delegateSegmentResults(*e.state.DelegateSegment)
		if delegateSegmentResultsErr != nil {
			return agent.Transition{}, delegateSegmentResultsErr
		}
		e.state.SettledToolResults = append(e.state.SettledToolResults, results...)
		e.state.ActiveToolCallEndIndex = 0
		e.state.DelegateSegment = nil
		return e.advanceToolCallBatch(consumedSignals)
	}
	waitKey, err := delegateWaitKey(e.state.ModelCallCount, *e.state.DelegateSegment)
	if err != nil {
		return agent.Transition{}, err
	}
	effect, err := agent.WaitForChildren(agent.ChildWaitSpec{
		Key: waitKey, Children: children, Condition: agent.AllChildren(),
	})
	if err != nil {
		return agent.Transition{}, err
	}
	e.state.Phase = phaseAwaitingDelegateWaitOpen
	return agent.Continue(consumedSignals, effect)
}

func (e *execution) acceptDelegateWaitOpen(signals []agent.Signal) (agent.Transition, error) {
	opened, steer, consumedSignals, err := collectChildWaitOpened(signals)
	if err != nil {
		return agent.Transition{}, err
	}
	if addSteerErr := e.addSteer(steer); addSteerErr != nil {
		return agent.Transition{}, addSteerErr
	}
	want, err := e.delegateWaitSpec()
	if err != nil {
		return agent.Transition{}, err
	}
	got := opened.Spec()
	if got.Key != want.Key || got.Condition != want.Condition || !slices.Equal(got.Children, want.Children) {
		return agent.Transition{}, fmt.Errorf("%w: Delegate child-wait opening mismatch", ErrInvalidExecutionState)
	}
	waitID := opened.WaitID()
	e.state.WaitID = &waitID
	e.state.Phase = phaseWaitingDelegates
	return agent.Wait(consumedSignals, waitID)
}

func (e *execution) acceptDelegates(signals []agent.Signal) (agent.Transition, error) {
	completed, steer, consumedSignals, err := collectChildrenCompleted(signals)
	if err != nil {
		return agent.Transition{}, err
	}
	if addSteerErr := e.addSteer(steer); addSteerErr != nil {
		return agent.Transition{}, addSteerErr
	}
	if e.state.WaitID == nil || completed.WaitID() != *e.state.WaitID {
		return agent.Transition{}, fmt.Errorf("%w: Delegate child completion addressed the wrong wait", ErrInvalidExecutionState)
	}
	want, err := e.delegateWaitSpec()
	if err != nil || completed.Key() != want.Key {
		return agent.Transition{}, fmt.Errorf("%w: Delegate child completion wait mismatch", ErrInvalidExecutionState)
	}
	calls, err := e.activeCallSegment()
	if err != nil {
		return agent.Transition{}, err
	}
	outcomes := completed.Outcomes()
	next := 0
	results := make([]chat.ToolResult, len(calls))
	artifacts := make([]artifactRecord, 0, len(calls))
	for index, invocation := range e.state.DelegateSegment.Invocations {
		if invocation.ToolResult != nil {
			results[index] = *invocation.ToolResult
			continue
		}
		if invocation.ChildKey == nil || invocation.ChildProcessID == nil || next >= len(outcomes) {
			return agent.Transition{}, fmt.Errorf("%w: missing Delegate child outcome", ErrInvalidExecutionState)
		}
		outcome := outcomes[next]
		next++
		if outcome.Key() != *invocation.ChildKey || outcome.Result().ProcessID() != *invocation.ChildProcessID {
			return agent.Transition{}, fmt.Errorf("%w: Delegate child outcome mismatch", ErrInvalidExecutionState)
		}
		result := outcome.Result()
		if result.Status() != agent.StatusCompleted {
			termination := result.Termination()
			diagnostic := "child ended with " + result.Status().String() +
				" (" + termination.Cause().String() + ")"
			if termination.Reason() != "" {
				diagnostic += ": " + termination.Reason()
			}
			results[index] = delegateErrorResult(calls[index], diagnostic)
			continue
		}
		output, present := result.Output()
		delegate, found := e.definition.delegate(calls[index].Name)
		if !present || !found || delegate.outputSchema.ValidateOutput(output) != nil {
			return agent.Transition{}, fmt.Errorf("%w: Delegate child output violates its frozen contract", ErrInvalidExecutionState)
		}
		toolOutput, outputErr := chat.NewJSONToolOutput(output.JSON())
		if outputErr != nil {
			return agent.Transition{}, fmt.Errorf("%w: encode Delegate Tool output: %w", ErrInvalidExecutionState, outputErr)
		}
		results[index] = chat.ToolResult{
			ID: calls[index].ID, Name: calls[index].Name, Output: toolOutput,
		}
		artifacts = append(artifacts, artifactRecord{
			ModelCallSequence: e.state.ModelCallCount,
			ToolCallIndex:     e.state.nextToolCallIndex() + uint32(index),
			ToolCallID:        calls[index].ID, DelegateName: calls[index].Name, Output: output,
		})
	}
	if next != len(outcomes) {
		return agent.Transition{}, fmt.Errorf("%w: unexpected Delegate child outcome", ErrInvalidExecutionState)
	}
	e.state.SettledToolResults = append(e.state.SettledToolResults, results...)
	e.state.ArtifactRecords = append(e.state.ArtifactRecords, artifacts...)
	e.state.ActiveToolCallEndIndex = 0
	e.state.DelegateSegment = nil
	e.state.WaitID = nil
	return e.advanceToolCallBatch(consumedSignals)
}

func (e *execution) delegateChildren() []agent.ProcessID {
	if e.state.DelegateSegment == nil {
		return nil
	}
	children := make([]agent.ProcessID, 0, len(e.state.DelegateSegment.Invocations))
	for _, invocation := range e.state.DelegateSegment.Invocations {
		if invocation.ChildProcessID != nil {
			children = append(children, *invocation.ChildProcessID)
		}
	}
	return children
}

func (e *execution) delegateWaitSpec() (agent.ChildWaitSpec, error) {
	if e.state.DelegateSegment == nil {
		return agent.ChildWaitSpec{}, ErrInvalidExecutionState
	}
	key, err := delegateWaitKey(e.state.ModelCallCount, *e.state.DelegateSegment)
	if err != nil {
		return agent.ChildWaitSpec{}, err
	}
	spec := agent.ChildWaitSpec{
		Key: key, Children: e.delegateChildren(), Condition: agent.AllChildren(),
	}
	if !spec.Valid() {
		return agent.ChildWaitSpec{}, ErrInvalidExecutionState
	}
	return spec, nil
}

func collectChildStarts(signals []agent.Signal) ([]agent.ChildStartResult, steerBatch, uint32, error) {
	starts := make([]agent.ChildStartResult, 0, len(signals))
	var steer steerBatch
	for _, signal := range signals {
		if recognized, err := appendSteerSignal(&steer, signal); err != nil {
			return nil, steerBatch{}, 0, err
		} else if recognized {
			continue
		}
		start, err := agent.ParseChildStartResult(signal)
		if err != nil {
			return nil, steerBatch{}, 0, fmt.Errorf("%w: invalid Delegate child-start Signal", ErrInvalidExecutionState)
		}
		starts = append(starts, start)
	}
	if len(starts) == 0 {
		return nil, steerBatch{}, 0, fmt.Errorf("%w: Delegate child-start Signal is missing", ErrInvalidExecutionState)
	}
	return starts, steer, uint32(len(signals)), nil
}

func collectChildWaitOpened(signals []agent.Signal) (agent.ChildWaitOpened, steerBatch, uint32, error) {
	var opened agent.ChildWaitOpened
	var found bool
	var steer steerBatch
	var consumed uint32
	for _, signal := range signals {
		if recognized, err := appendSteerSignal(&steer, signal); err != nil {
			return agent.ChildWaitOpened{}, steerBatch{}, 0, err
		} else if recognized {
			consumed++
			continue
		}
		value, err := agent.ParseChildWaitOpened(signal)
		if err == nil {
			if found {
				return agent.ChildWaitOpened{}, steerBatch{}, 0, fmt.Errorf("%w: duplicate Delegate wait-opened Signal", ErrInvalidExecutionState)
			}
			opened, found = value, true
			consumed++
			continue
		}
		if found {
			if _, completionErr := agent.ParseChildrenCompleted(signal); completionErr == nil {
				break
			}
		}
		return agent.ChildWaitOpened{}, steerBatch{}, 0, fmt.Errorf("%w: invalid Delegate wait-opened Signal", ErrInvalidExecutionState)
	}
	if !found {
		return agent.ChildWaitOpened{}, steerBatch{}, 0, fmt.Errorf("%w: Delegate wait-opened Signal is missing", ErrInvalidExecutionState)
	}
	return opened, steer, consumed, nil
}

func collectChildrenCompleted(signals []agent.Signal) (agent.ChildrenCompleted, steerBatch, uint32, error) {
	var completed agent.ChildrenCompleted
	var found bool
	var steer steerBatch
	for _, signal := range signals {
		if recognized, err := appendSteerSignal(&steer, signal); err != nil {
			return agent.ChildrenCompleted{}, steerBatch{}, 0, err
		} else if recognized {
			continue
		}
		value, err := agent.ParseChildrenCompleted(signal)
		if err != nil || found {
			return agent.ChildrenCompleted{}, steerBatch{}, 0, fmt.Errorf("%w: invalid or duplicate Delegate completion Signal", ErrInvalidExecutionState)
		}
		completed, found = value, true
	}
	if !found {
		return agent.ChildrenCompleted{}, steerBatch{}, 0, fmt.Errorf("%w: Delegate completion Signal is missing", ErrInvalidExecutionState)
	}
	return completed, steer, uint32(len(signals)), nil
}

func appendSteerSignal(batch *steerBatch, signal agent.Signal) (bool, error) {
	envelope, err := decodeSignal(signal.Payload())
	if err != nil {
		return false, nil
	}
	if envelope.Operation != operationSteer {
		return true, fmt.Errorf("%w: unexpected Interaction %q Signal", ErrInvalidExecutionState, envelope.Operation)
	}
	if err := batch.appendSignal(signal, envelope.Steer.Messages); err != nil {
		return true, fmt.Errorf("%w: %w", ErrInvalidExecutionState, err)
	}
	return true, nil
}

func delegateSegmentResults(segment delegateSegmentState) ([]chat.ToolResult, error) {
	results := make([]chat.ToolResult, len(segment.Invocations))
	for index, invocation := range segment.Invocations {
		if invocation.ToolResult == nil {
			return nil, fmt.Errorf("%w: Delegate call %d is not settled", ErrInvalidExecutionState, index)
		}
		results[index] = *invocation.ToolResult
	}
	return results, nil
}

func delegateErrorResult(call chat.ToolCall, diagnostic string) chat.ToolResult {
	return chat.ToolResult{
		ID: call.ID, Name: call.Name,
		Output: chat.NewTextToolOutput("error: delegated worker " + boundedDiagnostic(diagnostic)), IsError: true,
	}
}

// DelegateChildKey derives the exact managed ChildKey used for one Delegate
// ToolCall. Consumers can use the same value to correlate model observation
// with the child Process without exposing ToolCall to the Kernel.
func DelegateChildKey(modelCallSequence uint32, toolCall chat.ToolCall) (agent.ChildKey, error) {
	if modelCallSequence == 0 {
		return agent.ChildKey{}, fmt.Errorf("%w: model call sequence is required", ErrInvalidDelegate)
	}
	if err := toolCall.Validate(); err != nil {
		return agent.ChildKey{}, fmt.Errorf("%w: ToolCall: %w", ErrInvalidDelegate, err)
	}
	hash := sha256.New()
	hash.Write([]byte(strconv.FormatUint(uint64(modelCallSequence), 10)))
	hash.Write([]byte{0})
	hash.Write([]byte(toolCall.ID))
	hash.Write([]byte{0})
	hash.Write([]byte(toolCall.Name))
	return agent.ParseChildKey("interaction.delegate.child." + hex.EncodeToString(hash.Sum(nil)))
}

func delegateWaitKey(modelCallCount uint32, segment delegateSegmentState) (agent.WaitKey, error) {
	hash := sha256.New()
	hash.Write([]byte(strconv.FormatUint(uint64(modelCallCount), 10)))
	for _, invocation := range segment.Invocations {
		if invocation.ChildProcessID == nil || invocation.ChildKey == nil {
			continue
		}
		hash.Write([]byte{0})
		hash.Write([]byte(invocation.ChildKey.String()))
		hash.Write([]byte{0})
		hash.Write([]byte(invocation.ChildProcessID.String()))
	}
	return agent.ParseWaitKey("interaction.delegate.wait." + hex.EncodeToString(hash.Sum(nil)))
}
