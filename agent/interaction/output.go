package interaction

import (
	"fmt"

	"github.com/Tangerg/scope/core/chat"
)

// Output is the final semantic Interaction result. Exactly one completion is
// present: ModelResponse when the model produced a final response without
// requesting another tool round, or DirectToolResults when every call in one
// model-requested batch targeted a DirectResultTool and returned successfully.
// ModelResponse is accumulated independently of best-effort stream Delta
// delivery, so it remains complete after delta loss or snapshot restoration.
type Output struct {
	ModelResponse *chat.Response `json:"model_response,omitzero"`

	// DirectToolResults preserves model ToolCall order.
	DirectToolResults []chat.ToolResult `json:"direct_tool_results,omitempty"`

	// ModelCalls is the number of model Effects issued by this Interaction.
	ModelCalls uint64 `json:"model_calls"`
}

func (o Output) Validate() error {
	if o.ModelCalls == 0 {
		return fmt.Errorf("%w: output model_calls must be positive", ErrInvalidResult)
	}
	if (o.ModelResponse == nil) == (len(o.DirectToolResults) == 0) {
		return fmt.Errorf("%w: output needs exactly one of a model response and direct tool results", ErrInvalidResult)
	}
	if o.ModelResponse != nil {
		return o.validateModelResponse()
	}
	return o.validateDirectToolResults()
}

func (o Output) validateModelResponse() error {
	if err := o.ModelResponse.Validate(); err != nil {
		return fmt.Errorf("%w: output model response: %w", ErrInvalidResult, err)
	}
	message := finishedMessage(o.ModelResponse)
	if message == nil {
		return fmt.Errorf("%w: output has no finished assistant response", ErrInvalidResult)
	}
	for _, part := range message.Parts {
		if part.ToolCall != nil {
			return fmt.Errorf("%w: final model response contains a pending tool call", ErrInvalidResult)
		}
	}
	return nil
}

func (o Output) validateDirectToolResults() error {
	seen := make(map[string]struct{}, len(o.DirectToolResults))
	for index, result := range o.DirectToolResults {
		if err := result.Validate(); err != nil {
			return fmt.Errorf("%w: direct tool result %d: %w", ErrInvalidResult, index, err)
		}
		if result.IsError {
			return fmt.Errorf("%w: direct tool result %d failed", ErrInvalidResult, index)
		}
		if _, duplicate := seen[result.ID]; duplicate {
			return fmt.Errorf("%w: duplicate direct tool result ID %q", ErrInvalidResult, result.ID)
		}
		seen[result.ID] = struct{}{}
	}
	return nil
}

func (o Output) clone() Output {
	cloned := o
	cloned.ModelResponse = o.ModelResponse.Clone()
	cloned.DirectToolResults = cloneToolResults(o.DirectToolResults)
	return cloned
}

// finishedMessage is the assistant message a response finished with, or nil
// when the response has not finished one. Admitting a final model response and
// validating an Output apply this one rule.
func finishedMessage(response *chat.Response) *chat.Message {
	if response == nil || response.Output == nil || response.Output.FinishReason == "" {
		return nil
	}
	return response.Output.Message
}
