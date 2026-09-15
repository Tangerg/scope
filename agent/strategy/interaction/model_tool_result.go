package interaction

import (
	"context"
	"errors"
	"fmt"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

// modelToolResult projects only definite outcomes into model context. Unclassified
// errors and invalid output retain uncertainty at the Effect boundary.
func modelToolResult(call chat.ToolCall, output chat.ToolOutput, cause error) (chat.ToolResult, *toolInputRequest, bool, error) {
	if err := call.Validate(); err != nil {
		return chat.ToolResult{}, nil, false, errors.Join(fmt.Errorf("interaction: invalid tool call: %w", err), cause)
	}
	if cause == nil {
		if err := output.Validate(); err != nil {
			cause = fmt.Errorf("tool returned invalid output: %w", err)
		} else {
			return chat.ToolResult{ID: call.ID, Name: call.Name, Output: output.Clone()}, nil, false, nil
		}
	}
	if errors.Is(cause, ErrHostFailure) {
		return chat.ToolResult{}, nil, false, cause
	}
	if failure, ok := errors.AsType[*tool.Failure](cause); ok {
		if err := failure.Validate(); err != nil {
			return chat.ToolResult{}, nil, false, err
		}
		return chat.ToolResult{ID: call.ID, Name: call.Name, Output: failure.Output(), IsError: true}, nil, failure.Kind() == tool.FailureKindRejected, nil
	}
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		return chat.ToolResult{}, nil, false, cause
	}
	if inputRequired, ok := errors.AsType[*toolInputRequiredError](cause); ok {
		request, valid := inputRequired.inputRequest()
		if !valid {
			return chat.ToolResult{}, nil, false, ErrInvalidToolInputRequest
		}
		return chat.ToolResult{}, &request, false, nil
	}
	return chat.ToolResult{}, nil, false, cause
}

func rejectedToolResult(call chat.ToolCall, diagnostic string) chat.ToolResult {
	return chat.ToolResult{
		ID: call.ID, Name: call.Name, IsError: true,
		Output: chat.NewTextToolOutput("error: " + diagnostic),
	}
}
