package interaction

import (
	"context"
	"fmt"
	"math"

	"github.com/Tangerg/scope/core/chat"
)

type toolCallRound struct {
	Response   *chat.Response   `json:"response"`
	Results    []toolCallResult `json:"results,omitempty"`
	ChildBatch *childCallBatch  `json:"child_batch,omitzero"`
}

func (t *toolCallRound) nextCallIndex() uint32 { return uint32(len(t.Results)) }

// answered reports whether every call in the response has a result. It only
// counts calls; validation of the response and results is separate.
func (t *toolCallRound) answered() bool {
	if t.Response == nil || t.Response.Output == nil || t.Response.Output.Message == nil {
		return false
	}
	calls := 0
	for _, part := range t.Response.Output.Message.Parts {
		if part.Kind == chat.PartToolCall {
			calls++
		}
	}
	return calls != 0 && calls == len(t.Results)
}

func (t *toolCallRound) knownResult(index int) *toolCallResult {
	if index < len(t.Results) {
		return &t.Results[index]
	}
	offset := index - len(t.Results)
	if t.ChildBatch != nil && offset < len(t.ChildBatch.Invocations) {
		if invocation := t.ChildBatch.Invocations[offset]; invocation != nil {
			return invocation.Result
		}
	}
	return nil
}

func (t *toolCallRound) activeCalls(ctx context.Context) ([]chat.ToolCall, error) {
	if t == nil {
		return nil, fmt.Errorf("%w: active call phase requires a tool round", ErrInvalidExecutionState)
	}
	calls, err := validatedToolCalls(t.Response)
	if err != nil || len(calls) == 0 || uint64(len(calls)) > math.MaxUint32 {
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
		if invocation == nil || invocation.Result == nil {
			return nil, ErrInvalidExecutionState
		}
		names, err := tools.mergeAdvertisements(advertisedNames, invocation.Result.AdvertisedToolNames)
		if err != nil {
			return nil, err
		}
		advertisedNames = names
		// The Interaction's advertised set now owns these names.
		result := invocation.Result.clone()
		result.AdvertisedToolNames = nil
		results = append(results, result)
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
		if len(result.AdvertisedToolNames) != 0 {
			return fmt.Errorf("%w: result %d retains advertisements the Interaction already applied", ErrInvalidExecutionState, index)
		}
		if t.Response.Output.FinishReason == chat.FinishReasonLength && !result.Rejected {
			return fmt.Errorf("%w: truncated calls can only have rejected results", ErrInvalidExecutionState)
		}
	}
	return nil
}

func (t *toolCallRound) reject(call chat.ToolCall, diagnostic string) {
	result := newToolCallResult(rejectedToolResult(call, diagnostic))
	result.Rejected = true
	t.Results = append(t.Results, result)
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
