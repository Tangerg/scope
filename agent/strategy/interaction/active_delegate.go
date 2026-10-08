package interaction

import (
	"context"
	"fmt"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/core/chat"
)

// ActiveDelegateChild is the immutable Interaction-owned attribution of one
// model ToolCall to its currently active managed child Process. It contains no
// Engine handle, persistence identity, or Host metadata.
type ActiveDelegateChild struct {
	modelCallSequence uint64
	toolCallIndex     uint32
	toolCall          chat.ToolCall
	processID         agent.ProcessID
	parentProcessID   agent.ProcessID
}

// ModelCallSequence returns the one-based model call that requested the child.
func (a ActiveDelegateChild) ModelCallSequence() uint64 {
	return a.modelCallSequence
}

// ToolCallIndex returns the zero-based ToolCall position in the model response.
func (a ActiveDelegateChild) ToolCallIndex() uint32 { return a.toolCallIndex }

func (a ActiveDelegateChild) ToolCall() chat.ToolCall { return a.toolCall }

// ChildKey derives the child's key from the ToolCall that requested it.
func (a ActiveDelegateChild) ChildKey() agent.ChildKey {
	key, _ := DelegateChildKey(a.modelCallSequence, a.toolCall)
	return key
}

func (a ActiveDelegateChild) ProcessID() agent.ProcessID { return a.processID }

func (a ActiveDelegateChild) Reference() (ToolCallRef, bool) {
	if !a.Valid() {
		return ToolCallRef{}, false
	}
	return ToolCallRef{processID: a.parentProcessID, modelCallSequence: a.modelCallSequence, toolCallIndex: a.toolCallIndex}, true
}

func (a ActiveDelegateChild) Valid() bool {
	return a.modelCallSequence != 0 && a.toolCall.Validate() == nil &&
		a.processID.Valid() && a.parentProcessID.Valid()
}

// ActiveDelegateChildren interprets only Interaction-owned state.
// A valid snapshot without an active Interaction Delegate segment returns
// found=false. Returned children preserve model ToolCall order.
func ActiveDelegateChildren(
	snapshot agent.ProcessSnapshot,
) (children []ActiveDelegateChild, found bool, err error) {
	if !snapshot.Valid() {
		return nil, false, fmt.Errorf("%w: invalid Process snapshot", ErrInvalidExecutionState)
	}
	stateEnvelope := snapshot.CommittedExecutionState()
	if stateEnvelope.Kind() != executionStateKind {
		return nil, false, nil
	}
	state, decodeErr := stateEnvelope.Decode[executionState](executionStateKind)
	if decodeErr != nil {
		return nil, false, fmt.Errorf("%w: decode state: %w", ErrInvalidExecutionState, decodeErr)
	}
	if state.ToolRound == nil || state.ToolRound.ChildBatch == nil {
		return nil, false, nil
	}
	if envelopeErr := state.validateEnvelope(); envelopeErr != nil {
		return nil, false, envelopeErr
	}
	activeCalls, activeErr := state.activeChildCalls(context.Background())
	if activeErr != nil {
		return nil, false, fmt.Errorf("%w: active Delegate children: %w", ErrInvalidExecutionState, activeErr)
	}
	if state.ToolRound.ChildBatch.Kind != childCallsDelegate {
		return nil, false, nil
	}
	children = make([]ActiveDelegateChild, 0, len(activeCalls))
	for index, invocation := range state.ToolRound.ChildBatch.Invocations {
		if invocation == nil || invocation.ProcessID == nil {
			continue
		}
		child := ActiveDelegateChild{
			modelCallSequence: state.ModelCallCount,
			toolCallIndex:     state.ToolRound.nextCallIndex() + uint32(index),
			toolCall:          activeCalls[index],
			processID:         *invocation.ProcessID,
			parentProcessID:   snapshot.Relation().ProcessID(),
		}
		children = append(children, child)
	}
	return children, true, nil
}
