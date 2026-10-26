package chatclient

import (
	"fmt"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

// ToolBatchError preserves the original request, complete model proposal,
// successful prefix, and call at which a serial tool batch stopped. Completed
// effects have not been rolled back and later calls were not executed. The cause
// may be a [tool.Failure] with acknowledged partial effects. The enclosing
// [chat.CallFailure] is [chat.CallFailureFailed] for a definite Tool failure or
// an unsupported input checkpoint; otherwise it is [chat.CallFailureUnknown].
// Neither outcome assigns retry policy.
type ToolBatchError struct {
	completed    []chat.ToolResult
	request      *chat.Request
	proposal     *chat.Response
	cause        error
	inputRequest *tool.InputRequest
}

func (t *ToolBatchError) Error() string {
	return fmt.Sprintf("chatclient: tool batch failed at call[%d] %q: %v", len(t.completed), t.FailedCall().Name, t.cause)
}

func (t *ToolBatchError) Unwrap() error { return t.cause }

func (t *ToolBatchError) callFailure() *chat.CallFailure {
	kind := chat.CallFailureUnknown
	if t.inputRequest != nil {
		// The Tool reported a valid suspension, but this synchronous invocation
		// cannot continue it. Its failure does not complete the logical Tool call.
		kind = chat.CallFailureFailed
	} else if _, definite := tool.ClassifyFailure(t.cause); definite {
		kind = chat.CallFailureFailed
	}
	return &chat.CallFailure{Kind: kind, Cause: t}
}

// InputRequest retains a Tool checkpoint when this synchronous batch cannot
// continue it. It belongs to FailedCall and does not imply a completed ToolResult.
func (t *ToolBatchError) InputRequest() (tool.InputRequest, bool) {
	if t.inputRequest == nil {
		return tool.InputRequest{}, false
	}
	return *t.inputRequest, true
}

// Completed returns independently owned successful results in execution order.
func (t *ToolBatchError) Completed() []chat.ToolResult {
	results := make([]chat.ToolResult, len(t.completed))
	for index, result := range t.completed {
		results[index] = result.Clone()
	}
	return results
}

// FailedCall returns the original model proposal at which the batch stopped:
// the Tool failed, returned invalid output, or requested external input. The
// proposal owns it, at len(Completed()) among its tool calls.
func (t *ToolBatchError) FailedCall() chat.ToolCall {
	calls, _ := toolCalls(t.proposal)
	return calls[len(t.completed)]
}

// Request returns the frozen input to the model that proposed the batch.
func (t *ToolBatchError) Request() *chat.Request { return t.request.Clone() }

// Proposal returns the complete model response, including every ordered call,
// its original arguments, assistant content, and metadata. The failed call is
// at len(Completed()) among tool-call parts; every later call was not executed.
// Failure does not prove absence of side effects from the failed call.
func (t *ToolBatchError) Proposal() *chat.Response { return t.proposal.Clone() }
