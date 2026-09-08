package interaction

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/core/chat"
)

type toolInvocationState struct {
	ChildKey  *agent.ChildKey  `json:"child_key,omitempty"`
	ProcessID *agent.ProcessID `json:"process_id,omitempty"`
	Result    *toolCallResult  `json:"result,omitempty"`
}

type toolSegmentState struct {
	Invocations    []toolInvocationState `json:"invocations"`
	NextStartIndex uint32                `json:"next_start_index"`
}

func (t toolSegmentState) validate(current phase, calls []chat.ToolCall, modelSequence uint32, definition *Definition) error {
	if len(t.Invocations) != len(calls) || int(t.NextStartIndex) > len(calls) || !definition.tools.deploymentRef.Valid() {
		return fmt.Errorf("%w: invalid Tool child window", ErrInvalidExecutionState)
	}
	plans, err := definition.tools.planCalls(calls)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidExecutionState, err)
	}
	if concurrentBatchEnd(plans, 0) != len(calls) || definition.maxConcurrentToolCalls == 1 && len(calls) != 1 {
		return fmt.Errorf("%w: Tool window crosses an exclusive boundary", ErrInvalidExecutionState)
	}
	pending, active := 0, 0
	seen := make(map[agent.ProcessID]struct{})
	for index, invocation := range t.Invocations {
		if index >= int(t.NextStartIndex) {
			if invocation.ChildKey != nil || invocation.ProcessID != nil || invocation.Result != nil {
				return fmt.Errorf("%w: unstarted Tool has child state", ErrInvalidExecutionState)
			}
			continue
		}
		key, err := toolChildKey(modelSequence, calls[index])
		if err != nil || invocation.ChildKey == nil || *invocation.ChildKey != key {
			return fmt.Errorf("%w: Tool child key does not match its call", ErrInvalidExecutionState)
		}
		if invocation.ProcessID == nil {
			if invocation.Result != nil {
				return fmt.Errorf("%w: unstarted Tool has a result", ErrInvalidExecutionState)
			}
			pending++
			continue
		}
		if !invocation.ProcessID.Valid() {
			return ErrInvalidExecutionState
		}
		if _, duplicate := seen[*invocation.ProcessID]; duplicate {
			return fmt.Errorf("%w: duplicate Tool child Process", ErrInvalidExecutionState)
		}
		seen[*invocation.ProcessID] = struct{}{}
		if invocation.Result == nil {
			active++
			continue
		}
		if err := invocation.Result.validateCall(calls[index]); err != nil {
			return fmt.Errorf("%w: %w", ErrInvalidExecutionState, err)
		}

	}
	if pending+active > definition.maxConcurrentToolCalls ||
		(current == phaseAwaitingToolStarts && pending == 0) ||
		(current != phaseAwaitingToolStarts && (pending != 0 || active == 0)) {
		return fmt.Errorf("%w: Tool child phase disagrees with its active window", ErrInvalidExecutionState)
	}
	return nil
}

func (e *execution) startToolSegment(consumed uint32, calls []chat.ToolCall) (agent.Transition, bool, error) {
	start := e.state.NextToolCallIndex
	if _, found := e.definition.tools.entries[calls[start].Name]; !found {
		e.state.SettledToolResults = append(e.state.SettledToolResults, rejectedToolResult(calls[start], fmt.Sprintf("tool %q is not available", calls[start].Name)))
		e.state.DirectToolResultEligible = false
		e.state.NextToolCallIndex++
		return agent.Transition{}, false, nil
	}
	end := start + 1
	if e.definition.maxConcurrentToolCalls > 1 {
		for end < uint32(len(calls)) {
			if _, found := e.definition.tools.entries[calls[end].Name]; !found {
				break
			}
			end++
		}
		plans, err := e.definition.tools.planCalls(calls[start:end])
		if err != nil {
			return agent.Transition{}, false, err
		}
		end = start + uint32(concurrentBatchEnd(plans, 0))
	}
	e.state.ActiveToolCallEndIndex = end
	e.state.ToolSegment = &toolSegmentState{Invocations: make([]toolInvocationState, end-start)}
	transition, err := e.scheduleToolChildren(consumed)
	return transition, true, err
}

func (e *execution) scheduleToolChildren(consumed uint32) (agent.Transition, error) {
	calls, err := e.activeCallSegment()
	if err != nil {
		return agent.Transition{}, err
	}
	segment := e.state.ToolSegment
	active := len(e.toolChildren())
	var effects []agent.Effect
	for active < e.definition.maxConcurrentToolCalls && int(segment.NextStartIndex) < len(calls) {
		index := segment.NextStartIndex
		call := calls[index]
		key, keyErr := toolChildKey(e.state.ModelCallCount, call)
		if keyErr != nil {
			return agent.Transition{}, keyErr
		}
		input, inputErr := agent.EncodeInput(toolCall{
			ModelCallSequence: e.state.ModelCallCount, ToolCallIndex: e.state.NextToolCallIndex + index, Call: call,
		})
		if inputErr != nil {
			return agent.Transition{}, inputErr
		}
		effect, effectErr := agent.StartChild(agent.ChildSpec{
			Key: key, DeploymentRef: e.definition.tools.deploymentRef, Input: input,
			Budget: e.definition.toolBudget, Capabilities: e.definition.toolCapabilities,
		})
		if effectErr != nil {
			return agent.Transition{}, effectErr
		}
		segment.Invocations[index].ChildKey = &key
		segment.NextStartIndex++
		effects = append(effects, effect)
		active++
	}
	if len(effects) != 0 {
		e.state.Phase = phaseAwaitingToolStarts
		return agent.Continue(consumed, effects...)
	}
	if active != 0 {
		return e.waitForToolChildren(consumed)
	}
	for _, invocation := range segment.Invocations {
		if invocation.Result == nil || invocation.Result.Result == nil {
			return agent.Transition{}, ErrInvalidExecutionState
		}
		e.state.SettledToolResults = append(e.state.SettledToolResults, invocation.Result.Result.Clone())
		e.state.AdvertisedToolNames, err = mergeAdvertisedToolNames(e.state.AdvertisedToolNames, invocation.Result.AdvertisedToolNames)
		if err != nil {
			return agent.Transition{}, err
		}
		e.state.DirectToolResultEligible = e.state.DirectToolResultEligible && invocation.Result.Direct
	}
	e.state.NextToolCallIndex = e.state.ActiveToolCallEndIndex
	e.state.ActiveToolCallEndIndex = 0
	e.state.ToolSegment = nil
	return e.advanceToolCallBatch(consumed)
}

func (e *execution) acceptToolStarts(signals []agent.Signal) (agent.Transition, error) {
	starts, steer, consumed, err := collectChildStarts(signals)
	if err != nil {
		return agent.Transition{}, err
	}
	if steerErr := e.addSteer(steer); steerErr != nil {
		return agent.Transition{}, steerErr
	}
	next := 0
	for index := range e.state.ToolSegment.Invocations {
		invocation := &e.state.ToolSegment.Invocations[index]
		if invocation.ChildKey == nil || invocation.ProcessID != nil {
			continue
		}
		if next >= len(starts) {
			return agent.Transition{}, fmt.Errorf("%w: missing Tool child start", ErrInvalidExecutionState)
		}
		start := starts[next]
		next++
		if start.Key() != *invocation.ChildKey || start.DeploymentRef() != e.definition.tools.deploymentRef {
			return agent.Transition{}, ErrInvalidExecutionState
		}
		if failure, failed := start.Failure(); failed {
			return e.fail(consumed, agent.FailureKindExecution, "interaction.tool.start_failed", failure.Message())
		}
		processID, started := start.ProcessID()
		if !started {
			return agent.Transition{}, ErrInvalidExecutionState
		}
		invocation.ProcessID = &processID
	}
	if next != len(starts) {
		return agent.Transition{}, fmt.Errorf("%w: unexpected Tool child start", ErrInvalidExecutionState)
	}
	return e.waitForToolChildren(consumed)
}

func (e *execution) waitForToolChildren(consumed uint32) (agent.Transition, error) {
	spec, err := e.toolWaitSpec()
	if err != nil {
		return agent.Transition{}, err
	}
	effect, err := agent.WaitForChildren(spec)
	if err != nil {
		return agent.Transition{}, err
	}
	e.state.Phase = phaseAwaitingToolWaitOpen
	return agent.Continue(consumed, effect)
}

func (e *execution) acceptToolWaitOpen(signals []agent.Signal) (agent.Transition, error) {
	opened, steer, consumed, err := collectChildWaitOpened(signals)
	if err != nil {
		return agent.Transition{}, err
	}
	if steerErr := e.addSteer(steer); steerErr != nil {
		return agent.Transition{}, steerErr
	}
	want, err := e.toolWaitSpec()
	if err != nil {
		return agent.Transition{}, err
	}
	got := opened.Spec()
	if got.Key != want.Key || got.Condition != want.Condition || !slices.Equal(got.Children, want.Children) {
		return agent.Transition{}, fmt.Errorf("%w: Tool wait opening does not match active children", ErrInvalidExecutionState)
	}
	waitID := opened.WaitID()
	e.state.WaitID = &waitID
	e.state.Phase = phaseWaitingTools
	return agent.Wait(consumed, waitID)
}

func (e *execution) acceptToolCompletions(signals []agent.Signal) (agent.Transition, error) {
	completed, steer, consumed, err := collectChildrenCompleted(signals)
	if err != nil {
		return agent.Transition{}, err
	}
	if steerErr := e.addSteer(steer); steerErr != nil {
		return agent.Transition{}, steerErr
	}
	want, err := e.toolWaitSpec()
	if err != nil || e.state.WaitID == nil || completed.WaitID() != *e.state.WaitID || completed.Key() != want.Key {
		return agent.Transition{}, fmt.Errorf("%w: Tool completion wait mismatch", ErrInvalidExecutionState)
	}
	for _, outcome := range completed.Outcomes() {
		index := slices.IndexFunc(e.state.ToolSegment.Invocations, func(invocation toolInvocationState) bool {
			return invocation.ProcessID != nil && *invocation.ProcessID == outcome.Result().ProcessID()
		})
		if index < 0 {
			return agent.Transition{}, ErrInvalidExecutionState
		}
		invocation := &e.state.ToolSegment.Invocations[index]
		if invocation.Result != nil || invocation.ChildKey == nil || outcome.Key() != *invocation.ChildKey {
			return agent.Transition{}, ErrInvalidExecutionState
		}
		result := outcome.Result()
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
		calls, err := e.activeCallSegment()
		if err != nil {
			return agent.Transition{}, err
		}
		if err := decoded.validateCall(calls[index]); err != nil {
			return agent.Transition{}, fmt.Errorf("%w: %w", ErrInvalidExecutionState, err)
		}
		invocation.Result = &decoded
	}
	e.state.WaitID = nil
	return e.scheduleToolChildren(consumed)
}

func (e *execution) toolChildren() []agent.ProcessID {
	var children []agent.ProcessID
	for _, invocation := range e.state.ToolSegment.Invocations {
		if invocation.ProcessID != nil && invocation.Result == nil {
			children = append(children, *invocation.ProcessID)
		}
	}
	return children
}

func (e *execution) toolWaitSpec() (agent.ChildWaitSpec, error) {
	completed := 0
	for _, invocation := range e.state.ToolSegment.Invocations {
		if invocation.Result != nil {
			completed++
		}
	}
	key, err := agent.ParseWaitKey(fmt.Sprintf("tools:%d:%d:%d", e.state.ModelCallCount, e.state.NextToolCallIndex, completed))
	if err != nil {
		return agent.ChildWaitSpec{}, err
	}
	spec := agent.ChildWaitSpec{Key: key, Children: e.toolChildren(), Condition: agent.AnyChild()}
	if !spec.Valid() {
		return agent.ChildWaitSpec{}, ErrInvalidExecutionState
	}
	return spec, nil
}

func toolChildKey(modelSequence uint32, call chat.ToolCall) (agent.ChildKey, error) {
	if modelSequence == 0 || call.Validate() != nil {
		return agent.ChildKey{}, ErrInvalidExecutionState
	}
	digest := sha256.Sum256([]byte(fmt.Sprintf("%d:%s", modelSequence, call.ID)))
	return agent.ParseChildKey("tool_" + hex.EncodeToString(digest[:]))
}
