package interaction

import (
	"context"
	"fmt"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/strategy/internal/childcall"
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
				failure, err := agent.NewFailure(agent.FailureKindExternal, failureCodeInteractionDelegateUnresolvedEffects, agent.NormalizeDiagnostic(diagnostic))
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
		failure, err := agent.NewFailure(agent.FailureKindExecution, failureCodeInteractionToolProcessFailed, agent.NormalizeDiagnostic(diagnostic))
		return failure, true, err
	}
	return agent.Failure{}, false, nil
}

type childInvocationState struct {
	ChildKey  *agent.ChildKey  `json:"child_key,omitzero"`
	ProcessID *agent.ProcessID `json:"process_id,omitzero"`
	Result    *toolCallResult  `json:"result,omitzero"`
}

func (c childInvocationState) validate(kind childCallKind, key agent.ChildKey, call chat.ToolCall) error {
	if c.ChildKey != nil && *c.ChildKey != key {
		return fmt.Errorf("%w: child key does not match its call", ErrInvalidExecutionState)
	}
	if c.ChildKey == nil && (kind != childCallsDelegate || c.ProcessID != nil || c.Result == nil) {
		return fmt.Errorf("%w: planned child has no key", ErrInvalidExecutionState)
	}
	if c.ProcessID != nil && !c.ProcessID.Valid() {
		return ErrInvalidExecutionState
	}
	if c.Result == nil {
		return nil
	}
	return c.validateResult(kind, call)
}

func (c childInvocationState) validateResult(kind childCallKind, call chat.ToolCall) error {
	if err := c.Result.validateCall(call); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidExecutionState, err)
	}
	if kind == childCallsDelegate {
		if c.ProcessID != nil || !c.Result.Result.IsError || c.Result.Direct || len(c.Result.AdvertisedToolNames) != 0 {
			return fmt.Errorf("%w: pending Delegate batch retains a completed child", ErrInvalidExecutionState)
		}
		return nil
	}
	if c.ProcessID == nil {
		return fmt.Errorf("%w: unstarted Tool has a result", ErrInvalidExecutionState)
	}
	return nil
}

func (c childInvocationState) empty() bool {
	return c.ChildKey == nil && c.ProcessID == nil && c.Result == nil
}

// One batch owns child admission, the active wait, and ordered settlements.
// Kind selects binding and scheduling policy without creating another protocol.
type childCallBatch struct {
	Kind           childCallKind          `json:"kind"`
	Invocations    []childInvocationState `json:"invocations"`
	NextStartIndex uint32                 `json:"next_start_index"`
	WaitID         *agent.WaitID          `json:"wait_id,omitzero"`
}

func (c childCallBatch) validate(ctx context.Context, current phase, calls []chat.ToolCall, modelSequence uint64) error {
	if err := c.validateShape(calls); err != nil {
		return err
	}
	if err := c.validateWait(current); err != nil {
		return err
	}
	pending, active, err := c.unsettledCounts(ctx, calls, modelSequence)
	if err != nil {
		return err
	}
	if current == phaseAwaitingChildStarts {
		if pending == 0 || c.Kind == childCallsDelegate && active != 0 {
			return fmt.Errorf("%w: child starts disagree with the active batch", ErrInvalidExecutionState)
		}
		return nil
	}
	if pending != 0 || active == 0 {
		return fmt.Errorf("%w: child wait disagrees with the active batch", ErrInvalidExecutionState)
	}
	return nil
}

func (c childCallBatch) validateShape(calls []chat.ToolCall) error {
	if c.Kind != childCallsTool && c.Kind != childCallsDelegate || len(calls) == 0 ||
		len(c.Invocations) != len(calls) || c.NextStartIndex == 0 || uint64(c.NextStartIndex) > uint64(len(calls)) {
		return fmt.Errorf("%w: invalid child call batch", ErrInvalidExecutionState)
	}
	if c.Kind == childCallsDelegate && int(c.NextStartIndex) != len(calls) {
		return fmt.Errorf("%w: Delegate batch has unplanned calls", ErrInvalidExecutionState)
	}
	return nil
}

func (c childCallBatch) validateWait(current phase) error {
	if current != phaseWaitingChildren {
		if c.WaitID != nil {
			return fmt.Errorf("%w: child start or wait opening already has a WaitID", ErrInvalidExecutionState)
		}
	} else if c.WaitID == nil || !c.WaitID.Valid() {
		return fmt.Errorf("%w: waiting children require an Engine WaitID", ErrInvalidExecutionState)
	}
	if err := c.protocolBatch(nil).Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidExecutionState, err)
	}
	return nil
}

func (c childCallBatch) unsettledCounts(ctx context.Context, calls []chat.ToolCall, modelSequence uint64) (pending, active int, err error) {
	for index, invocation := range c.Invocations {
		if err := ctx.Err(); err != nil {
			return 0, 0, err
		}
		if index >= int(c.NextStartIndex) {
			if !invocation.empty() {
				return 0, 0, fmt.Errorf("%w: unplanned call has child state", ErrInvalidExecutionState)
			}
			continue
		}
		key, err := c.childKey(modelSequence, calls[index])
		if err != nil {
			return 0, 0, fmt.Errorf("%w: child key does not match its call", ErrInvalidExecutionState)
		}
		if err := invocation.validate(c.Kind, key, calls[index]); err != nil {
			return 0, 0, err
		}
		switch {
		case invocation.Result != nil:
		case invocation.ProcessID == nil:
			pending++
		default:
			active++
		}
	}
	return pending, active, nil
}

func (c childCallBatch) childKey(modelSequence uint64, call chat.ToolCall) (agent.ChildKey, error) {
	if c.Kind == childCallsDelegate {
		return DelegateChildKey(modelSequence, call)
	}
	return ToolChildKey(modelSequence, call)
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
	if !definition.tools.deploymentRef.Valid() || end != len(calls) ||
		definition.maxConcurrentToolCalls == 1 && len(calls) != 1 {
		return fmt.Errorf("%w: Tool batch crosses an exclusive boundary", ErrInvalidExecutionState)
	}
	active := 0
	for _, invocation := range c.Invocations {
		if err := ctx.Err(); err != nil {
			return err
		}
		if invocation.Result != nil {
			if err := definition.tools.validateAdvertisements(invocation.Result.AdvertisedToolNames); err != nil {
				return fmt.Errorf("%w: child advertisements: %w", ErrInvalidExecutionState, err)
			}
		}
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

func (c childCallBatch) waitSpec(modelSequence uint64, callIndex uint32) (agent.ChildWaitSpec, error) {
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
	return c.protocolBatch(nil).WaitSpec(key, agent.ChildWaitBoundaryDrained, condition)
}

func (c childCallBatch) protocolBatch(bindings []agent.DeploymentRef) childcall.Batch {
	batch := childcall.Batch{Children: make([]childcall.Child, len(c.Invocations))}
	if c.WaitID != nil {
		batch.WaitID = *c.WaitID
	}
	for index, invocation := range c.Invocations {
		child := &batch.Children[index]
		child.Done = index >= int(c.NextStartIndex) || invocation.Result != nil
		if invocation.ChildKey != nil {
			child.Key = *invocation.ChildKey
		}
		if invocation.ProcessID != nil {
			child.ProcessID = *invocation.ProcessID
		}
		if len(bindings) == len(c.Invocations) {
			child.Deployment = bindings[index]
		}
	}
	return batch
}

func (c *childCallBatch) acceptStarts(starts []agent.ChildStartResult, bindings []agent.DeploymentRef) ([]int, error) {
	batch := c.protocolBatch(bindings)
	if len(bindings) != len(c.Invocations) || len(starts) != batch.PendingStarts() {
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

func (c *childCallBatch) acceptWaitOpened(opened agent.ChildWaitOpened, want agent.ChildWaitSpec) error {
	waitID, err := c.protocolBatch(nil).AcceptOpening(opened, want.Key, want.Boundary, want.Condition)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidExecutionState, err)
	}
	c.WaitID = &waitID
	return nil
}

func (c childCallBatch) validateCompletions(completed agent.ChildWaitSatisfied, want agent.ChildWaitSpec) ([]int, error) {
	indices, err := c.protocolBatch(nil).Complete(completed, want.Key, want.Boundary, want.Condition)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidExecutionState, err)
	}
	return indices, nil
}
