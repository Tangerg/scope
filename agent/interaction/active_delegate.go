package interaction

import (
	jsonv2 "encoding/json/v2"
	"fmt"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/core/chat"
)

// ActiveDelegateChild is the immutable Interaction-owned attribution of one
// model ToolCall to its currently active managed child Process. It contains no
// Engine handle, persistence identity, or Host metadata.
type ActiveDelegateChild struct {
	modelCallSequence uint32
	toolCallIndex     uint32
	toolCall          chat.ToolCall
	childKey          agent.ChildKey
	processID         agent.ProcessID
}

// ModelCallSequence returns the one-based model call that requested the child.
func (a ActiveDelegateChild) ModelCallSequence() uint32 {
	return a.modelCallSequence
}

// ToolCallIndex returns the zero-based ToolCall position in the model response.
func (a ActiveDelegateChild) ToolCallIndex() uint32 { return a.toolCallIndex }

// ToolCall returns the exact model ToolCall represented by the child.
func (a ActiveDelegateChild) ToolCall() chat.ToolCall { return a.toolCall }

// ChildKey returns the parent-scoped logical child identity.
func (a ActiveDelegateChild) ChildKey() agent.ChildKey { return a.childKey }

// ProcessID returns the Engine-minted child Process identity.
func (a ActiveDelegateChild) ProcessID() agent.ProcessID { return a.processID }

func (a ActiveDelegateChild) Valid() bool {
	if a.modelCallSequence == 0 || a.toolCall.Validate() != nil ||
		!a.childKey.Valid() || !a.processID.Valid() {
		return false
	}
	key, err := DelegateChildKey(a.modelCallSequence, a.toolCall)
	return err == nil && key == a.childKey
}

// ActiveDelegateChildrenFromSnapshot interprets only Interaction-owned state.
// A valid snapshot without an active Interaction Delegate segment returns
// found=false. Returned children preserve model ToolCall order.
func ActiveDelegateChildrenFromSnapshot(
	snapshot agent.ProcessSnapshot,
) (children []ActiveDelegateChild, found bool, err error) {
	if !snapshot.Valid() {
		return nil, false, fmt.Errorf("%w: invalid Process snapshot", ErrInvalidExecutionState)
	}
	stateEnvelope := snapshot.CommittedExecutionState()
	if stateEnvelope.Kind() != executionStateKind {
		return nil, false, nil
	}
	var state executionState
	if decodeErr := jsonv2.Unmarshal(stateEnvelope.Payload(), &state, jsonv2.RejectUnknownMembers(true)); decodeErr != nil {
		return nil, false, fmt.Errorf("%w: decode state: %w", ErrInvalidExecutionState, decodeErr)
	}
	if state.ChildBatch == nil {
		return nil, false, nil
	}
	activeCalls, activeErr := state.activeChildCalls()
	if activeErr != nil {
		return nil, false, fmt.Errorf("%w: active Delegate children: %w", ErrInvalidExecutionState, activeErr)
	}
	if state.ChildBatch.Kind != childCallsDelegate {
		return nil, false, nil
	}
	children = make([]ActiveDelegateChild, 0, len(activeCalls))
	for index, invocation := range state.ChildBatch.Invocations {
		if invocation.ProcessID == nil {
			continue
		}
		call := activeCalls[index]
		child := ActiveDelegateChild{
			modelCallSequence: state.ModelCallCount,
			toolCallIndex:     state.nextToolCallIndex() + uint32(index),
			toolCall:          call,
			childKey:          *invocation.ChildKey,
			processID:         *invocation.ProcessID,
		}
		children = append(children, child)
	}
	return children, true, nil
}
