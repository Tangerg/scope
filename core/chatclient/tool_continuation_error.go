package chatclient

import (
	"fmt"

	"github.com/Tangerg/scope/core/chat"
)

// ToolContinuationError reports a failed model call after every tool in the
// batch completed. The completed effects have not been rolled back. Callers
// may retry Request through the downstream model without rerunning tools;
// passing it through NewToolMiddleware again violates that middleware's tool
// ownership contract. The error assigns no retry policy.
type ToolContinuationError struct {
	request *chat.Request
	cause   error
}

func (t *ToolContinuationError) Error() string {
	return fmt.Sprintf("chatclient: model continuation failed after tool execution: %v", t.cause)
}

func (t *ToolContinuationError) Unwrap() error { return t.cause }

// Request returns an independently owned continuation, including the original
// messages, tool proposals, completed results, and frozen model settings.
func (t *ToolContinuationError) Request() *chat.Request { return t.request.Clone() }

// Completed returns independently owned results in execution order.
func (t *ToolContinuationError) Completed() []chat.ToolResult {
	parts := t.request.Messages[len(t.request.Messages)-1].Parts
	results := make([]chat.ToolResult, len(parts))
	for index, part := range parts {
		results[index] = part.ToolResult.Clone()
	}
	return results
}
