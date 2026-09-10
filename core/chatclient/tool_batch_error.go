package chatclient

import (
	"fmt"

	"github.com/Tangerg/scope/core/chat"
)

// ToolBatchError preserves the successful prefix of a serial tool batch and
// the first failed call. Completed effects have not been rolled back. The
// failure cause may itself be a tool.Failure with acknowledged partial effects;
// later calls were not executed. This fact assigns no retry policy.
type ToolBatchError struct {
	completed []chat.ToolResult
	failed    chat.ToolCall
	cause     error
}

func (t *ToolBatchError) Error() string {
	return fmt.Sprintf("chatclient: tool batch failed at call[%d] %q: %v", len(t.completed), t.failed.Name, t.cause)
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
func (t *ToolBatchError) FailedCall() chat.ToolCall { return t.failed }
