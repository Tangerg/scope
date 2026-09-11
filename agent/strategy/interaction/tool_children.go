package interaction

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/core/chat"
)

func (e *execution) startToolChildren(ctx context.Context, consumed uint32, calls []chat.ToolCall) (agent.Transition, error) {
	start := e.state.ToolRound.nextCallIndex()
	count := 1
	if e.definition.maxConcurrentToolCalls > 1 {
		var err error
		count, err = e.definition.tools.concurrentBatchEnd(ctx, calls[start:])
		if err != nil {
			return agent.Transition{}, err
		}
	}
	e.state.ToolRound.beginChildren(&childCallBatch{Kind: childCallsTool, Invocations: make([]childInvocationState, count)})
	return e.scheduleToolChildren(ctx, consumed)
}

func (e *execution) scheduleToolChildren(ctx context.Context, consumed uint32) (agent.Transition, error) {
	calls, err := e.state.ToolRound.activeCalls()
	if err != nil {
		return agent.Transition{}, err
	}
	batch := e.state.ToolRound.ChildBatch
	active := len(e.state.ToolRound.ChildBatch.children())
	var effects []agent.Effect
	for active < e.definition.maxConcurrentToolCalls && int(batch.NextStartIndex) < len(calls) {
		if err := ctx.Err(); err != nil {
			return agent.Transition{}, err
		}
		index := batch.NextStartIndex
		call := calls[index]
		key, keyErr := toolChildKey(e.state.ModelCallCount, call)
		if keyErr != nil {
			return agent.Transition{}, keyErr
		}
		input, inputErr := agent.EncodeInput(toolCall{
			ModelCallSequence: e.state.ModelCallCount, ToolCallIndex: e.state.ToolRound.nextCallIndex() + index, Call: call,
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
		batch.Invocations[index].ChildKey = &key
		batch.NextStartIndex++
		effects = append(effects, effect)
		active++
	}
	if len(effects) != 0 {
		e.state.Phase = phaseAwaitingChildStarts
		return agent.Continue(consumed, effects...)
	}
	if active != 0 {
		return e.waitForChildren(consumed)
	}
	if err := e.finishChildBatch(); err != nil {
		return agent.Transition{}, err
	}
	return e.advanceToolCallBatch(ctx, consumed)
}

func toolChildKey(modelSequence uint32, call chat.ToolCall) (agent.ChildKey, error) {
	if modelSequence == 0 || call.Validate() != nil {
		return agent.ChildKey{}, ErrInvalidExecutionState
	}
	digest := sha256.Sum256([]byte(fmt.Sprintf("%d:%s", modelSequence, call.ID)))
	return agent.ParseChildKey("tool_" + hex.EncodeToString(digest[:]))
}
