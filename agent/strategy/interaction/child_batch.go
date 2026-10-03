package interaction

import (
	"context"
	"fmt"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/strategy/internal/childcall"
	"github.com/Tangerg/scope/agent/strategy/internal/stepfail"
	"github.com/Tangerg/scope/core/chat"
)

type childCallKind string

const (
	childCallsTool     childCallKind = "tool"
	childCallsDelegate childCallKind = "delegate"
)

// terminalFailure applies the kind's policy to drained outcomes: an ordinary
// Tool child is required infrastructure, so any unsuccessful end fails the
// parent, while a Delegate's own failure is model-visible and only unresolved
// Effects in its drained subtree fail the parent.
func (c childCallKind) terminalFailure(outcomes []agent.ChildOutcome) (agent.Failure, bool, error) {
	for _, outcome := range outcomes {
		result := outcome.Result()
		if c == childCallsDelegate {
			if !outcome.SubtreeResolved() {
				unresolved, _ := outcome.SubtreeUnresolvedEffects()
				diagnostic := fmt.Sprintf("Delegate subtree %s ended with unresolved Effects %v", result.ProcessID(), unresolved)
				failure, err := stepfail.Failure(agent.FailureKindExternal, failureCodeInteractionDelegateUnresolvedEffects, diagnostic)
				return failure, true, err
			}
			continue
		}
		if result.Status() == agent.StatusCompleted {
			continue
		}
		termination := result.Termination()
		if failure, failed := termination.Failure(); failed {
			return failure, true, nil
		}
		diagnostic := fmt.Sprintf("Tool child %s ended with %s (%s): %s", result.ProcessID(), result.Status(), termination.Cause(), termination.Reason())
		failure, err := stepfail.Failure(agent.FailureKindExecution, failureCodeInteractionToolProcessFailed, diagnostic)
		return failure, true, err
	}
	return agent.Failure{}, false, nil
}

// A nil invocation has not been requested. Declaring its start creates the
// record that then retains admission and settlement; no separate marker can
// disagree with those facts. ChildKey follows from the call and model sequence.
type childInvocationState struct {
	ProcessID *agent.ProcessID `json:"process_id,omitzero"`
	Result    *toolCallResult  `json:"result,omitzero"`
}

func (c *childInvocationState) validate(kind childCallKind, call chat.ToolCall) error {
	if c.ProcessID != nil && !c.ProcessID.Valid() {
		return ErrInvalidExecutionState
	}
	if c.Result == nil {
		return nil
	}
	return c.validateResult(kind, call)
}

func (c *childInvocationState) validateResult(kind childCallKind, call chat.ToolCall) error {
	if err := c.Result.validateCall(call); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidExecutionState, err)
	}
	if kind == childCallsDelegate {
		if c.ProcessID != nil || !c.Result.IsError || c.Result.Direct || len(c.Result.AdvertisedToolNames) != 0 {
			return fmt.Errorf("%w: pending Delegate batch retains a completed child", ErrInvalidExecutionState)
		}
		return nil
	}
	if c.ProcessID == nil {
		return fmt.Errorf("%w: unstarted Tool has a result", ErrInvalidExecutionState)
	}
	return nil
}

// One batch owns child admission, the active wait, and ordered settlements.
// Kind selects binding and scheduling policy without creating another protocol.
type childCallBatch struct {
	Kind        childCallKind           `json:"kind"`
	Invocations []*childInvocationState `json:"invocations"`
	WaitID      *agent.WaitID           `json:"wait_id,omitzero"`
}

func (c childCallBatch) nextStartIndex() int {
	for index, invocation := range c.Invocations {
		if invocation == nil {
			return index
		}
	}
	return len(c.Invocations)
}

// phase follows admission: starts settle before the wait opens, and only an
// accepted opening records its WaitID.
func (c childCallBatch) phase() phase {
	for _, invocation := range c.Invocations {
		if invocation != nil && invocation.ProcessID == nil && invocation.Result == nil {
			return phaseAwaitingChildStarts
		}
	}
	if c.WaitID != nil {
		return phaseWaitingChildren
	}
	return phaseAwaitingChildWaitOpen
}

func (c childCallBatch) validate(ctx context.Context, calls []chat.ToolCall, modelCallSequence uint64) error {
	if err := c.validateShape(calls); err != nil {
		return err
	}
	keys, err := c.childKeys(modelCallSequence, calls)
	if err != nil {
		return err
	}
	if err = c.validateWait(keys); err != nil {
		return err
	}
	active, err := c.activeChildren(ctx, calls)
	if err != nil {
		return err
	}
	// A Delegate batch starts as one admission; a Tool batch refills its window.
	if c.phase() == phaseAwaitingChildStarts {
		if c.Kind == childCallsDelegate && active != 0 {
			return fmt.Errorf("%w: Delegate batch has both pending and started children", ErrInvalidExecutionState)
		}
		return nil
	}
	if active == 0 {
		return fmt.Errorf("%w: child wait has no active children", ErrInvalidExecutionState)
	}
	return nil
}

func (c childCallBatch) validateShape(calls []chat.ToolCall) error {
	planned := c.nextStartIndex()
	if c.Kind != childCallsTool && c.Kind != childCallsDelegate || len(calls) == 0 ||
		len(c.Invocations) != len(calls) || planned == 0 {
		return fmt.Errorf("%w: invalid child call batch", ErrInvalidExecutionState)
	}
	if c.Kind == childCallsDelegate && planned != len(calls) {
		return fmt.Errorf("%w: Delegate batch has unplanned calls", ErrInvalidExecutionState)
	}
	return nil
}

func (c childCallBatch) validateWait(keys []agent.ChildKey) error {
	if c.WaitID != nil && !c.WaitID.Valid() {
		return fmt.Errorf("%w: waiting children require an Engine WaitID", ErrInvalidExecutionState)
	}
	if err := c.protocolBatch(keys).Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidExecutionState, err)
	}
	return nil
}

// activeChildren validates every planned invocation and counts the started
// children that have no result yet.
func (c childCallBatch) activeChildren(ctx context.Context, calls []chat.ToolCall) (int, error) {
	active := 0
	planned := c.nextStartIndex()
	for index, invocation := range c.Invocations {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		if index >= planned {
			if invocation != nil {
				return 0, fmt.Errorf("%w: unplanned call has child state", ErrInvalidExecutionState)
			}
			continue
		}
		if err := invocation.validate(c.Kind, calls[index]); err != nil {
			return 0, err
		}
		if invocation.ProcessID != nil && invocation.Result == nil {
			active++
		}
	}
	return active, nil
}

// childKeys derives each requested invocation's ChildKey from the call it
// serves; unrequested invocations have none.
func (c childCallBatch) childKeys(modelCallSequence uint64, calls []chat.ToolCall) ([]agent.ChildKey, error) {
	if len(calls) != len(c.Invocations) {
		return nil, fmt.Errorf("%w: child batch does not match its calls", ErrInvalidExecutionState)
	}
	keys := make([]agent.ChildKey, len(c.Invocations))
	for index, invocation := range c.Invocations {
		if invocation == nil {
			continue
		}
		key, err := c.childKey(modelCallSequence, calls[index])
		if err != nil {
			return nil, fmt.Errorf("%w: requested call has no child key: %w", ErrInvalidExecutionState, err)
		}
		keys[index] = key
	}
	return keys, nil
}

func (c childCallBatch) childKey(modelCallSequence uint64, call chat.ToolCall) (agent.ChildKey, error) {
	if c.Kind == childCallsDelegate {
		return DelegateChildKey(modelCallSequence, call)
	}
	return ToolChildKey(modelCallSequence, call)
}

func (c childCallBatch) validateBindings(ctx context.Context, definition *Definition, calls []chat.ToolCall) error {
	for _, call := range calls {
		if err := ctx.Err(); err != nil {
			return err
		}
		_, delegated := definition.delegate(call.Name)
		if delegated != (c.Kind == childCallsDelegate) {
			return fmt.Errorf("%w: child batch mixes Tool and Delegate ownership", ErrInvalidExecutionState)
		}
		if _, found := definition.tools.entries[call.Name]; !delegated && !found {
			return fmt.Errorf("%w: child batch references an unavailable Tool", ErrInvalidExecutionState)
		}
	}
	if c.Kind == childCallsDelegate {
		return nil
	}
	return c.validateToolWindow(ctx, definition, calls)
}

func (c childCallBatch) validateToolWindow(ctx context.Context, definition *Definition, calls []chat.ToolCall) error {
	end, err := definition.tools.concurrentBatchEnd(ctx, calls)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidExecutionState, err)
	}
	if !definition.toolDeployment.Valid() || end != len(calls) ||
		definition.maxConcurrentToolCalls == 1 && len(calls) != 1 {
		return fmt.Errorf("%w: Tool batch crosses an exclusive boundary", ErrInvalidExecutionState)
	}
	active := 0
	for _, invocation := range c.Invocations {
		if err := ctx.Err(); err != nil {
			return err
		}
		if invocation == nil {
			continue
		}
		if invocation.Result != nil {
			if err := definition.tools.validateAdvertisements(invocation.Result.AdvertisedToolNames); err != nil {
				return fmt.Errorf("%w: child advertisements: %w", ErrInvalidExecutionState, err)
			}
		}
		if invocation.Result == nil {
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
		if invocation != nil && invocation.ProcessID != nil && invocation.Result == nil {
			children = append(children, *invocation.ProcessID)
		}
	}
	return children
}

func (c childCallBatch) waitSpec(modelCallSequence uint64, callIndex uint32, keys []agent.ChildKey) (agent.ChildWaitSpec, error) {
	completed := 0
	for _, invocation := range c.Invocations {
		if invocation != nil && invocation.Result != nil {
			completed++
		}
	}
	key, err := agent.ParseWaitKey(fmt.Sprintf("interaction.children:%d:%d:%d", modelCallSequence, callIndex, completed))
	if err != nil {
		return agent.ChildWaitSpec{}, err
	}
	condition := agent.AnyChild()
	if c.Kind == childCallsDelegate {
		condition = agent.AllChildren()
	}
	return c.protocolBatch(keys).WaitSpec(key, agent.ChildWaitBoundaryDrained, condition)
}

func (c childCallBatch) protocolBatch(keys []agent.ChildKey) childcall.Batch {
	batch := childcall.Batch{Children: make([]childcall.Child, len(c.Invocations))}
	if c.WaitID != nil {
		batch.WaitID = *c.WaitID
	}
	for index, invocation := range c.Invocations {
		child := &batch.Children[index]
		if invocation == nil {
			child.Done = true
			continue
		}
		child.Done = invocation.Result != nil
		if len(keys) == len(c.Invocations) {
			child.Key = keys[index]
		}
		if invocation.ProcessID != nil {
			child.ProcessID = *invocation.ProcessID
		}
	}
	return batch
}

func (c *childCallBatch) acceptStarts(starts []agent.ChildStartResult, keys []agent.ChildKey) ([]int, error) {
	batch := c.protocolBatch(keys)
	if len(starts) != batch.PendingStarts() {
		return nil, fmt.Errorf("%w: child start count does not match the pending batch", ErrInvalidExecutionState)
	}
	indices, err := batch.AcceptStarts(starts)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidExecutionState, err)
	}
	for offset, index := range indices {
		if processID, started := starts[offset].ProcessID(); started {
			c.Invocations[index].ProcessID = &processID
		}
	}
	return indices, nil
}

func (c *childCallBatch) acceptWaitOpened(opened agent.ChildWaitOpened, keys []agent.ChildKey) error {
	waitID, err := c.protocolBatch(keys).AcceptOpening(opened)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidExecutionState, err)
	}
	c.WaitID = &waitID
	return nil
}

func (c childCallBatch) validateCompletions(completed agent.ChildWaitSatisfied, want agent.ChildWaitSpec, keys []agent.ChildKey) ([]int, error) {
	indices, err := c.protocolBatch(keys).Complete(completed, want.Key, want.Boundary, want.Condition)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidExecutionState, err)
	}
	return indices, nil
}
