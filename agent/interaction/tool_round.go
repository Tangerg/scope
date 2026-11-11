package interaction

import (
	"context"
	"errors"
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

// calls are the round's proposed ToolCalls. Decoding validated the response
// and validateProposal admitted its shape, so readers never check it again.
func (t *toolCallRound) calls() []chat.ToolCall { return proposedCalls(t.Response) }

// batchCalls are the calls the open batch executes, in proposal order.
func (t *toolCallRound) batchCalls() []chat.ToolCall {
	start := t.nextCallIndex()
	return t.calls()[start : start+uint32(len(t.Batch.Invocations))]
}

// validateProposal admits a restored round's response: present, finishing as
// a round, and proposing a bounded nonempty set of calls.
func (t *toolCallRound) validateProposal() error {
	if t.Response == nil {
		return errors.New("a tool round requires its model response")
	}
	calls := t.calls()
	if len(calls) == 0 || uint64(len(calls)) > math.MaxUint32 {
		return errors.New("a tool round requires a bounded nonempty set of tool calls")
	}
	if !opensToolRound(t.Response.Output.FinishReason) {
		return fmt.Errorf("finish reason %q does not open a tool round", t.Response.Output.FinishReason)
	}
	return nil
}

// answered reports whether every proposed call has a result.
func (t *toolCallRound) answered() bool { return len(t.Results) == len(t.calls()) }

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

// validateBatchCursor admits a restored batch's position: only an executed
// round opens batches, and the batch covers calls the results have not.
func (t *toolCallRound) validateBatchCursor() error {
	if t.truncated() {
		return fmt.Errorf("%w: a truncated round only rejects its calls", ErrInvalidExecutionState)
	}
	if len(t.Batch.Invocations) == 0 || uint64(len(t.Results))+uint64(len(t.Batch.Invocations)) > uint64(len(t.calls())) {
		return fmt.Errorf("%w: ToolCall cursor is inconsistent", ErrInvalidExecutionState)
	}
	return nil
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
		result := *invocation.Result
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
