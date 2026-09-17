package interaction

import (
	"context"
	"fmt"

	"github.com/Tangerg/scope/core/chat"
)

type toolCallRound struct {
	Response   *chat.Response   `json:"response"`
	Results    []toolCallResult `json:"results,omitempty"`
	ChildBatch *childCallBatch  `json:"child_batch,omitzero"`
}

func (t *toolCallRound) nextCallIndex() uint32 { return uint32(len(t.Results)) }

func (t *toolCallRound) knownResult(index int) *toolCallResult {
	if index < len(t.Results) {
		return &t.Results[index]
	}
	offset := index - len(t.Results)
	if t.ChildBatch != nil && offset < len(t.ChildBatch.Invocations) {
		return t.ChildBatch.Invocations[offset].Result
	}
	return nil
}

func (t *toolCallRound) activeCalls(ctx context.Context) ([]chat.ToolCall, error) {
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
	if err := t.validateResults(ctx, calls); err != nil {
		return nil, err
	}
	return calls[t.nextCallIndex() : t.nextCallIndex()+uint32(len(t.ChildBatch.Invocations))], nil
}

func (t *toolCallRound) beginChildren(batch *childCallBatch) {
	t.ChildBatch = batch
}

func (t *toolCallRound) finishChildren(tools toolManifest, advertisedNames []string) ([]string, error) {
	results := make([]toolCallResult, 0, len(t.ChildBatch.Invocations))
	for _, invocation := range t.ChildBatch.Invocations {
		if invocation.Result == nil {
			return nil, ErrInvalidExecutionState
		}
		names, err := tools.mergeAdvertisements(advertisedNames, invocation.Result.AdvertisedToolNames)
		if err != nil {
			return nil, err
		}
		advertisedNames = names
		results = append(results, invocation.Result.clone())
	}
	t.Results = append(t.Results, results...)
	t.ChildBatch = nil
	return advertisedNames, nil
}

func (t *toolCallRound) validateResults(ctx context.Context, calls []chat.ToolCall) error {
	if len(t.Results) > len(calls) {
		return fmt.Errorf("%w: Tool results exceed calls", ErrInvalidExecutionState)
	}
	for index, result := range t.Results {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := result.validateCall(calls[index]); err != nil {
			return fmt.Errorf("%w: result %d: %w", ErrInvalidExecutionState, index, err)
		}
		if t.Response.Output.FinishReason == chat.FinishReasonLength && !result.Rejected {
			return fmt.Errorf("%w: truncated calls can only have rejected results", ErrInvalidExecutionState)
		}
	}
	return nil
}

func (t *toolCallRound) reject(call chat.ToolCall, diagnostic string) {
	t.Results = append(t.Results, toolCallResult{Result: rejectedToolResult(call, diagnostic), Rejected: true})
}

func (t *toolCallRound) validateComplete(ctx context.Context) error {
	if t == nil || t.ChildBatch != nil || t.Response == nil || t.Response.Output == nil {
		return ErrInvalidExecutionState
	}
	finish := t.Response.Output.FinishReason
	if finish != chat.FinishReasonToolCalls && finish != chat.FinishReasonLength {
		return ErrInvalidExecutionState
	}
	calls, err := validatedToolCalls(t.Response)
	if err != nil || len(calls) == 0 || len(calls) != len(t.Results) {
		return fmt.Errorf("%w: round requires every call result", ErrInvalidExecutionState)
	}
	if err := t.validateResults(ctx, calls); err != nil {
		return err
	}
	return ctx.Err()
}
