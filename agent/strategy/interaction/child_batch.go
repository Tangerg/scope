package interaction

import (
	"fmt"
	"slices"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/strategy/internal/childcall"
	"github.com/Tangerg/scope/core/chat"
)

type childCallKind string

const (
	childCallsTool     childCallKind = "tool"
	childCallsDelegate childCallKind = "delegate"
)

type childInvocationState struct {
	ChildKey  *agent.ChildKey  `json:"child_key,omitempty"`
	ProcessID *agent.ProcessID `json:"process_id,omitempty"`
	Result    *toolCallResult  `json:"result,omitempty"`
}

// One batch owns child admission, the active wait, and ordered settlements.
// Kind selects binding and scheduling policy without creating another protocol.
type childCallBatch struct {
	Kind           childCallKind          `json:"kind"`
	Invocations    []childInvocationState `json:"invocations"`
	NextStartIndex uint32                 `json:"next_start_index"`
	WaitID         *agent.WaitID          `json:"wait_id,omitempty"`
}

func (c childCallBatch) validate(current phase, calls []chat.ToolCall, modelSequence uint32) error {
	if c.Kind != childCallsTool && c.Kind != childCallsDelegate || len(calls) == 0 ||
		len(c.Invocations) != len(calls) || c.NextStartIndex == 0 || uint64(c.NextStartIndex) > uint64(len(calls)) {
		return fmt.Errorf("%w: invalid child call batch", ErrInvalidExecutionState)
	}
	if c.Kind == childCallsDelegate && int(c.NextStartIndex) != len(calls) {
		return fmt.Errorf("%w: Delegate batch has unplanned calls", ErrInvalidExecutionState)
	}
	if current == phaseWaitingChildren {
		if c.WaitID == nil || !c.WaitID.Valid() {
			return fmt.Errorf("%w: waiting children require an Engine WaitID", ErrInvalidExecutionState)
		}
	} else if c.WaitID != nil {
		return fmt.Errorf("%w: child start or wait opening already has a WaitID", ErrInvalidExecutionState)
	}
	pending, active := 0, 0
	seen := make(map[agent.ProcessID]struct{})
	for index, invocation := range c.Invocations {
		if index >= int(c.NextStartIndex) {
			if invocation.ChildKey != nil || invocation.ProcessID != nil || invocation.Result != nil {
				return fmt.Errorf("%w: unplanned call has child state", ErrInvalidExecutionState)
			}
			continue
		}
		key, err := c.childKey(modelSequence, calls[index])
		if err != nil || invocation.ChildKey != nil && *invocation.ChildKey != key {
			return fmt.Errorf("%w: child key does not match its call", ErrInvalidExecutionState)
		}
		if invocation.ChildKey == nil && (c.Kind != childCallsDelegate || invocation.ProcessID != nil || invocation.Result == nil) {
			return fmt.Errorf("%w: planned child has no key", ErrInvalidExecutionState)
		}
		if invocation.ProcessID != nil {
			if !invocation.ProcessID.Valid() {
				return ErrInvalidExecutionState
			}
			if _, duplicate := seen[*invocation.ProcessID]; duplicate {
				return fmt.Errorf("%w: duplicate child Process", ErrInvalidExecutionState)
			}
			seen[*invocation.ProcessID] = struct{}{}
		}
		if invocation.Result != nil {
			if err := invocation.Result.validateCall(calls[index]); err != nil {
				return fmt.Errorf("%w: %w", ErrInvalidExecutionState, err)
			}
			if c.Kind == childCallsDelegate {
				if invocation.ProcessID != nil || !invocation.Result.Result.IsError ||
					invocation.Result.Direct || len(invocation.Result.AdvertisedToolNames) != 0 {
					return fmt.Errorf("%w: pending Delegate batch retains a completed child", ErrInvalidExecutionState)
				}
			} else if invocation.ProcessID == nil {
				return fmt.Errorf("%w: unstarted Tool has a result", ErrInvalidExecutionState)
			}
			continue
		}
		if invocation.ProcessID == nil {
			pending++
		} else {
			active++
		}
	}
	if current == phaseAwaitingChildStarts {
		if pending == 0 || c.Kind == childCallsDelegate && active != 0 {
			return fmt.Errorf("%w: child starts disagree with the active batch", ErrInvalidExecutionState)
		}
	} else if pending != 0 || active == 0 {
		return fmt.Errorf("%w: child wait disagrees with the active batch", ErrInvalidExecutionState)
	}
	return nil
}

func (c childCallBatch) childKey(modelSequence uint32, call chat.ToolCall) (agent.ChildKey, error) {
	if c.Kind == childCallsDelegate {
		return DelegateChildKey(modelSequence, call)
	}
	return toolChildKey(modelSequence, call)
}

func (c childCallBatch) validateBindings(definition *Definition, calls []chat.ToolCall) error {
	for _, call := range calls {
		_, delegated := definition.delegate(call.Name)
		if delegated != (c.Kind == childCallsDelegate) {
			return fmt.Errorf("%w: child batch mixes Tool and Delegate ownership", ErrInvalidExecutionState)
		}
		if c.Kind == childCallsTool {
			if _, found := definition.tools.entries[call.Name]; !found {
				return fmt.Errorf("%w: child batch references an unavailable Tool", ErrInvalidExecutionState)
			}
		}
	}
	if c.Kind == childCallsDelegate {
		return nil
	}
	plans, err := definition.tools.planCalls(calls)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidExecutionState, err)
	}
	if !definition.tools.deploymentRef.Valid() || concurrentBatchEnd(plans, 0) != len(calls) ||
		definition.maxConcurrentToolCalls == 1 && len(calls) != 1 {
		return fmt.Errorf("%w: Tool batch crosses an exclusive boundary", ErrInvalidExecutionState)
	}
	active := 0
	for _, invocation := range c.Invocations {
		if invocation.ChildKey != nil && invocation.Result == nil {
			active++
		}
	}
	if active > definition.maxConcurrentToolCalls {
		return fmt.Errorf("%w: Tool batch exceeds its concurrency limit", ErrInvalidExecutionState)
	}
	return nil
}

func (c childCallBatch) children() []agent.ProcessID {
	var children []agent.ProcessID
	for _, invocation := range c.Invocations {
		if invocation.ProcessID != nil && invocation.Result == nil {
			children = append(children, *invocation.ProcessID)
		}
	}
	return children
}

func (c childCallBatch) waitSpec(modelSequence, callIndex uint32) (agent.ChildWaitSpec, error) {
	completed := 0
	for _, invocation := range c.Invocations {
		if invocation.Result != nil {
			completed++
		}
	}
	key, err := agent.ParseWaitKey(fmt.Sprintf("interaction.children:%d:%d:%d", modelSequence, callIndex, completed))
	if err != nil {
		return agent.ChildWaitSpec{}, err
	}
	condition := agent.AnyChild()
	if c.Kind == childCallsDelegate {
		condition = agent.AllChildren()
	}
	spec := agent.ChildWaitSpec{Boundary: agent.ChildWaitBoundaryDrained, Key: key, Children: c.children(), Condition: condition}
	if !spec.Valid() {
		return agent.ChildWaitSpec{}, ErrInvalidExecutionState
	}
	return spec, nil
}

func (c *childCallBatch) acceptStarts(starts []agent.ChildStartResult, bindings []agent.DeploymentRef) ([]int, error) {
	var pending []int
	seen := make(map[agent.ProcessID]struct{})
	for index, invocation := range c.Invocations {
		if invocation.ProcessID != nil {
			seen[*invocation.ProcessID] = struct{}{}
		} else if invocation.ChildKey != nil && invocation.Result == nil {
			pending = append(pending, index)
		}
	}
	if len(starts) != len(pending) || len(bindings) != len(c.Invocations) {
		return nil, fmt.Errorf("%w: child start count does not match the pending batch", ErrInvalidExecutionState)
	}
	for offset, index := range pending {
		start := starts[offset]
		if !childcall.StartMatches(start, *c.Invocations[index].ChildKey, bindings[index]) {
			return nil, fmt.Errorf("%w: child start does not match its binding", ErrInvalidExecutionState)
		}
		if processID, started := start.ProcessID(); started {
			if _, duplicate := seen[processID]; duplicate {
				return nil, fmt.Errorf("%w: duplicate child Process", ErrInvalidExecutionState)
			}
			seen[processID] = struct{}{}
		}
	}
	for offset, index := range pending {
		if processID, started := starts[offset].ProcessID(); started {
			c.Invocations[index].ProcessID = &processID
		}
	}
	return pending, nil
}

func (c *childCallBatch) acceptWaitOpened(opened agent.ChildWaitOpened, want agent.ChildWaitSpec) error {
	if c.WaitID != nil || !childcall.OpeningMatches(opened, want) {
		return fmt.Errorf("%w: child wait opening does not match the active batch", ErrInvalidExecutionState)
	}
	waitID := opened.WaitID()
	c.WaitID = &waitID
	return nil
}

func (c childCallBatch) validateCompletions(completed agent.ChildWaitSatisfied, want agent.ChildWaitSpec) ([]int, error) {
	if c.WaitID == nil || !childcall.CompletionMatches(completed, *c.WaitID, want.Key, want.Boundary) {
		return nil, fmt.Errorf("%w: child completion wait mismatch", ErrInvalidExecutionState)
	}
	outcomes := completed.Outcomes()
	if len(outcomes) == 0 || c.Kind == childCallsDelegate && len(outcomes) != len(want.Children) {
		return nil, fmt.Errorf("%w: child completion count does not satisfy the active wait", ErrInvalidExecutionState)
	}
	indices := make([]int, 0, len(outcomes))
	previous := -1
	for _, outcome := range outcomes {
		index := slices.IndexFunc(c.Invocations, func(invocation childInvocationState) bool {
			return invocation.ProcessID != nil && *invocation.ProcessID == outcome.Result().ProcessID()
		})
		if index <= previous || c.Invocations[index].Result != nil ||
			c.Invocations[index].ChildKey == nil || outcome.Key() != *c.Invocations[index].ChildKey {
			return nil, fmt.Errorf("%w: child outcome does not match the active batch", ErrInvalidExecutionState)
		}
		indices = append(indices, index)
		previous = index
	}
	return indices, nil
}
