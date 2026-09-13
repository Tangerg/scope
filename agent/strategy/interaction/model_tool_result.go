package interaction

import (
	"errors"
	"fmt"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

// modelToolResult projects only definite outcomes into model context. Unclassified
// errors and invalid output retain uncertainty at the Effect boundary.
func modelToolResult(call chat.ToolCall, output chat.ToolOutput, cause error) (chat.ToolResult, error) {
	if err := call.Validate(); err != nil {
		return chat.ToolResult{}, errors.Join(fmt.Errorf("interaction: invalid tool call: %w", err), cause)
	}
	if cause == nil {
		if err := output.Validate(); err != nil {
			cause = fmt.Errorf("tool returned invalid output: %w", err)
		} else {
			return chat.ToolResult{ID: call.ID, Name: call.Name, Output: output.Clone()}, nil
		}
	}
	if isHostOrContextError(cause) {
		return chat.ToolResult{}, cause
	}
	if _, inputRequired := errors.AsType[*toolInputRequiredError](cause); inputRequired {
		return chat.ToolResult{}, cause
	}
	if errors.Is(cause, tool.ErrAuthorizationDenied) {
		return rejectedToolResult(call, fmt.Sprintf("tool %q is not authorized", call.Name)), nil
	}
	if failure, ok := errors.AsType[*tool.Failure](cause); ok {
		return chat.ToolResult{ID: call.ID, Name: call.Name, Output: failure.Output(), IsError: true}, nil
	}
	return chat.ToolResult{}, cause
}

func rejectedToolResult(call chat.ToolCall, diagnostic string) chat.ToolResult {
	return chat.ToolResult{
		ID: call.ID, Name: call.Name, IsError: true,
		Output: chat.NewTextToolOutput("error: " + diagnostic),
	}
}
