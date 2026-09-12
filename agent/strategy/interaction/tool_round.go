package interaction

import (
	"fmt"

	"github.com/Tangerg/scope/core/chat"
)

type toolCallRound struct {
	Response             *chat.Response    `json:"response"`
	Results              []chat.ToolResult `json:"results,omitempty"`
	DirectResultEligible bool              `json:"direct_result_eligible,omitempty"`
	ChildBatch           *childCallBatch   `json:"child_batch,omitempty"`
}

func (t *toolCallRound) nextCallIndex() uint32 { return uint32(len(t.Results)) }

func (t *toolCallRound) activeCalls() ([]chat.ToolCall, error) {
	if t == nil {
		return nil, fmt.Errorf("%w: active call phase requires a tool round", ErrInvalidExecutionState)
	}
	calls, err := validatedToolCalls(t.Response)
	if err != nil || len(calls) == 0 || uint64(len(calls)) > uint64(^uint32(0)) {
		return nil, fmt.Errorf("%w: tool round has no bounded unambiguous tool calls", ErrInvalidExecutionState)
	}
	if t.Response.Output.FinishReason != chat.FinishReasonToolCalls {
		return nil, fmt.Errorf("%w: tool round response must finish with tool_calls", ErrInvalidExecutionState)
	}
	if t.ChildBatch == nil || len(t.ChildBatch.Invocations) == 0 ||
		uint64(len(t.Results))+uint64(len(t.ChildBatch.Invocations)) > uint64(len(calls)) {
		return nil, fmt.Errorf("%w: ToolCall cursor is inconsistent", ErrInvalidExecutionState)
	}
	for index, result := range t.Results {
		if result.ID != calls[index].ID || result.Name != calls[index].Name {
			return nil, fmt.Errorf("%w: tool result %d does not match call %q", ErrInvalidExecutionState, index, calls[index].ID)
		}
	}
	return calls[t.nextCallIndex() : t.nextCallIndex()+uint32(len(t.ChildBatch.Invocations))], nil
}

func (t *toolCallRound) beginChildren(batch *childCallBatch) {
	t.ChildBatch = batch
	t.DirectResultEligible = t.DirectResultEligible && batch.Kind == childCallsTool
}

func (t *toolCallRound) rejectCall(call chat.ToolCall, diagnostic string) {
	t.Results = append(t.Results, rejectedToolResult(call, diagnostic))
	t.DirectResultEligible = false
}

func (t *toolCallRound) finishChildren(tools toolManifest, advertisedNames []string) ([]string, error) {
	results := make([]chat.ToolResult, 0, len(t.ChildBatch.Invocations))
	direct := t.DirectResultEligible
	for _, invocation := range t.ChildBatch.Invocations {
		if invocation.Result == nil || invocation.Result.Result == nil {
			return nil, ErrInvalidExecutionState
		}
		names, err := tools.mergeAdvertisements(advertisedNames, invocation.Result.AdvertisedToolNames)
		if err != nil {
			return nil, err
		}
		advertisedNames = names
		results = append(results, invocation.Result.Result.Clone())
		direct = direct && invocation.Result.Direct
	}
	t.Results = append(t.Results, results...)
	t.DirectResultEligible = direct
	t.ChildBatch = nil
	return advertisedNames, nil
}
