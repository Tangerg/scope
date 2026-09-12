package chatclient

import (
	"fmt"

	"github.com/Tangerg/scope/core/chat"
)

// ToolBatchError preserves the original request, complete model proposal,
// successful prefix, and failed position of a serial tool batch. Completed
// effects have not been rolled back. The failure cause may itself be a tool.Failure with acknowledged partial effects;
// later calls were not executed. This fact assigns no retry policy.
type ToolBatchError struct {
	completed []chat.ToolResult
	request   *chat.Request
	proposal  *chat.Response
	cause     error
}

func (t *ToolBatchError) Error() string {
	return fmt.Sprintf("chatclient: tool batch failed at call[%d] %q: %v", len(t.completed), t.FailedCall().Name, t.cause)
}

func (t *ToolBatchError) Unwrap() error { return t.cause }

// Completed returns independently owned successful results in execution order.
func (t *ToolBatchError) Completed() []chat.ToolResult {
	results := make([]chat.ToolResult, len(t.completed))
	for index, result := range t.completed {
		results[index] = result.Clone()
	}
	return results
}

// FailedCall returns the original model proposal that failed during execution
// or returned an invalid output. Its position is len(Completed()).
func (t *ToolBatchError) FailedCall() chat.ToolCall {
	index := 0
	for _, part := range t.proposal.Output.Message.Parts {
		if part.Kind != chat.PartToolCall {
			continue
		}
		if index == len(t.completed) {
			return *part.ToolCall
		}
		index++
	}
	panic("chatclient: tool batch failure has no failed proposal")
}

// Request returns the frozen input to the model that proposed the batch.
func (t *ToolBatchError) Request() *chat.Request { return t.request.Clone() }

// Proposal returns the complete model response, including every ordered call,
// its original arguments, assistant content, and metadata. The failed call is
// at len(Completed()) among tool-call parts; every later call was not executed.
// Failure does not prove absence of side effects from the failed call.
func (t *ToolBatchError) Proposal() *chat.Response { return t.proposal.Clone() }
