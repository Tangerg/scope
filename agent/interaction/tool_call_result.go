package interaction

import (
	"fmt"
	"slices"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

// toolCallResult is what executing one ToolCall adds: its output, disposition,
// and advertisements. The call owns the ID and name that the model-facing
// chat.ToolResult carries, and the frozen ToolSet owns whether a successful
// result completes directly, so neither is stored here. Disposition is decided
// once, where the outcome is classified; every rendering reads it.
type toolCallResult struct {
	Disposition         ResultDisposition `json:"disposition"`
	Output              tool.Output       `json:"output"`
	AdvertisedToolNames []string          `json:"advertised_tool_names,omitempty"`
}

func succeededCallResult(output tool.Output) toolCallResult {
	return toolCallResult{Disposition: ResultSucceeded, Output: output.Clone()}
}

func failedCallResult(output tool.Output) toolCallResult {
	return toolCallResult{Disposition: ResultFailed, Output: output.Clone()}
}

// rejectedCallResult refuses a call before its Tool runs; diagnostic is the
// complete model-visible explanation.
func rejectedCallResult(diagnostic string) toolCallResult {
	return toolCallResult{Disposition: ResultRejected, Output: tool.NewTextOutput("error: " + diagnostic)}
}

// settleToolCall projects only definite outcomes into model context. Unclassified
// errors and invalid output retain uncertainty at the Effect boundary.
func settleToolCall(outcome tool.Result, cause error) (toolDispatchResult, error) {
	if cause == nil {
		if err := outcome.Validate(); err != nil {
			return toolDispatchResult{}, fmt.Errorf("tool returned invalid output: %w", err)
		}
		if outcome.InputRequest != nil {
			return toolDispatchResult{InputRequest: new(*outcome.InputRequest)}, nil
		}
		return toolDispatchResult{Completion: new(succeededCallResult(outcome.Output))}, nil
	}
	failure, definite := tool.ClassifyFailure(cause)
	if !definite {
		return toolDispatchResult{}, cause
	}
	result := failedCallResult(failure.Output())
	if failure.Kind() == tool.FailureKindRejected {
		result.Disposition = ResultRejected
	}
	return toolDispatchResult{Completion: &result}, nil
}

// toolResult renders the result the model sees for call.
func (t toolCallResult) toolResult(call chat.ToolCall) chat.ToolResult {
	return chat.ToolResult{ID: call.ID, Name: call.Name, Output: t.Output.Clone(), IsError: t.Disposition != ResultSucceeded}
}

func (t toolCallResult) clone() toolCallResult {
	t.Output = t.Output.Clone()
	t.AdvertisedToolNames = slices.Clone(t.AdvertisedToolNames)
	return t
}

func (t toolCallResult) validate() error {
	if !t.Disposition.Valid() {
		return fmt.Errorf("%w: tool_result disposition is invalid", ErrInvalidProtocol)
	}
	if err := t.Output.Validate(); err != nil {
		return fmt.Errorf("%w: tool_result: %w", ErrInvalidProtocol, err)
	}
	if t.Disposition != ResultSucceeded && len(t.AdvertisedToolNames) != 0 {
		return fmt.Errorf("%w: failed tool_result cannot advertise Tools", ErrInvalidProtocol)
	}
	if err := validateAdvertisedToolNames(t.AdvertisedToolNames); err != nil {
		return fmt.Errorf("%w: tool_result advertised Tools: %w", ErrInvalidProtocol, err)
	}
	return nil
}

func (t toolCallResult) validateCall(call chat.ToolCall) error {
	if err := t.validate(); err != nil {
		return err
	}
	if err := t.toolResult(call).Validate(); err != nil {
		return fmt.Errorf("%w: tool_result: %w", ErrInvalidProtocol, err)
	}
	return nil
}
