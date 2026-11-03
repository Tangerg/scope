package interaction

import (
	"context"
	"fmt"
	"math"

	"github.com/Tangerg/scope/core/chat"
)

type toolCallRound struct {
	Response *chat.Response   `json:"response"`
	Results  []toolCallResult `json:"results,omitempty"`
	Batch    *callBatch       `json:"batch,omitzero"`
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

func (t *toolCallRound) rejected() bool {
	if !t.answered() {
		return false
	}
	for _, result := range t.Results {
		if result.Disposition != ResultRejected {
			return false
		}
	}
	return true
}

// opensToolRound reports whether a response that requested tool calls opens a
// round for them: a tool_calls finish executes them, while a length finish
// truncated them, so the round only rejects them.
func opensToolRound(finish chat.FinishReason) bool {
	return finish == chat.FinishReasonToolCalls || finish == chat.FinishReasonLength
}

func (t *toolCallRound) truncated() bool {
	return t.Response.Output.FinishReason == chat.FinishReasonLength
}

func (t *toolCallRound) activeCalls(ctx context.Context) ([]chat.ToolCall, error) {
	if t == nil {
		return nil, fmt.Errorf("%w: active call phase requires a tool round", ErrInvalidExecutionState)
	}
	calls, err := validatedToolCalls(t.Response)
	if err != nil || len(calls) == 0 || uint64(len(calls)) > math.MaxUint32 {
		return nil, fmt.Errorf("%w: tool round has no bounded unambiguous tool calls", ErrInvalidExecutionState)
	}
	if !opensToolRound(t.Response.Output.FinishReason) || t.truncated() {
		return nil, fmt.Errorf("%w: tool round response must finish with tool_calls", ErrInvalidExecutionState)
	}
	if t.Batch == nil || len(t.Batch.Invocations) == 0 ||
		uint64(len(t.Results))+uint64(len(t.Batch.Invocations)) > uint64(len(calls)) {
		return nil, fmt.Errorf("%w: ToolCall cursor is inconsistent", ErrInvalidExecutionState)
	}
	if err := t.validateResults(ctx, calls); err != nil {
		return nil, err
	}
	return calls[t.nextCallIndex() : t.nextCallIndex()+uint32(len(t.Batch.Invocations))], nil
}

func (t *toolCallRound) beginCalls(batch *callBatch) {
	t.Batch = batch
}

func (t *toolCallRound) finishCalls(tools toolManifest, advertisedNames []string) ([]string, error) {
	results := make([]toolCallResult, 0, len(t.Batch.Invocations))
	for _, invocation := range t.Batch.Invocations {
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
	t.Batch = nil
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
		if t.truncated() && result.Disposition != ResultRejected {
			return fmt.Errorf("%w: truncated calls can only have rejected results", ErrInvalidExecutionState)
		}
	}
	return nil
}

func (t *toolCallRound) reject(diagnostic string) {
	t.Results = append(t.Results, rejectedCallResult(diagnostic))
}

func (t *toolCallRound) validateComplete(ctx context.Context) error {
	if t == nil || t.Batch != nil || t.Response == nil || t.Response.Output == nil {
		return ErrInvalidExecutionState
	}
	if !opensToolRound(t.Response.Output.FinishReason) {
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
