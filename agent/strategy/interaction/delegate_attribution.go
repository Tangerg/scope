package interaction

import (
	"context"
	"fmt"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/core/chat"
)

// DelegateAttribution is the immutable Interaction-owned attribution of one
// model ToolCall to the child Process it was delegated to. It is read from the
// parent's committed tool round, so it persists into terminal snapshots and
// records only the delegation relationship; it does not assert the child is
// still running. Deciding whether a child is active requires its runtime status
// from a same-moment TreeSnapshot. It contains no Engine handle, persistence
// identity, or Host metadata.
type DelegateAttribution struct {
	modelCallSequence uint64
	toolCallIndex     uint32
	toolCall          chat.ToolCall
	processID         agent.ProcessID
	parentProcessID   agent.ProcessID
}

// ModelCallSequence returns the one-based model call that requested the child.
func (a DelegateAttribution) ModelCallSequence() uint64 {
	return a.modelCallSequence
}

// ToolCallIndex returns the zero-based ToolCall position in the model response.
func (a DelegateAttribution) ToolCallIndex() uint32 { return a.toolCallIndex }

func (a DelegateAttribution) ToolCall() chat.ToolCall { return a.toolCall }

// ChildKey derives the child's key from the ToolCall that requested it.
func (a DelegateAttribution) ChildKey() agent.ChildKey {
	key, _ := DelegateChildKey(a.modelCallSequence, a.toolCall)
	return key
}

func (a DelegateAttribution) ProcessID() agent.ProcessID { return a.processID }

func (a DelegateAttribution) Reference() (ToolCallRef, bool) {
	if !a.Valid() {
		return ToolCallRef{}, false
	}
	return ToolCallRef{processID: a.parentProcessID, modelCallSequence: a.modelCallSequence, toolCallIndex: a.toolCallIndex}, true
}

func (a DelegateAttribution) Valid() bool {
	return a.modelCallSequence != 0 && a.toolCall.Validate() == nil &&
		a.processID.Valid() && a.parentProcessID.Valid()
}

// DelegateAttributions interprets only Interaction-owned committed state and
// returns the delegation relationships from the parent's tool round in model
// ToolCall order. A valid snapshot without an Interaction Delegate segment
// returns found=false. Because it reads committed state, it also reports
// relationships whose child Processes have since finished or been canceled;
// callers that need current child liveness must consult a TreeSnapshot.
func DelegateAttributions(
	snapshot agent.ProcessSnapshot,
) (attributions []DelegateAttribution, found bool, err error) {
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
		return nil, false, fmt.Errorf("%w: delegate attributions: %w", ErrInvalidExecutionState, activeErr)
	}
	if state.ToolRound.ChildBatch.Kind != childCallsDelegate {
		return nil, false, nil
	}
	attributions = make([]DelegateAttribution, 0, len(activeCalls))
	for index, invocation := range state.ToolRound.ChildBatch.Invocations {
		if invocation == nil || invocation.ProcessID == nil {
			continue
		}
		attributions = append(attributions, DelegateAttribution{
			modelCallSequence: state.ModelCallCount,
			toolCallIndex:     state.ToolRound.nextCallIndex() + uint32(index),
			toolCall:          activeCalls[index],
			processID:         *invocation.ProcessID,
			parentProcessID:   snapshot.Relation().ProcessID(),
		})
	}
	return attributions, true, nil
}
