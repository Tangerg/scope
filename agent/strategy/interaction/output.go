package interaction

import (
	"errors"
	"fmt"

	"github.com/Tangerg/scope/core/chat"
)

// CompletionSource identifies the semantic value that completed an
// Interaction. It is Strategy-owned and does not add a Framework lifecycle
// status.
type CompletionSource string

const (
	// CompletionSourceModelResponse means the model produced a final response
	// without requesting another tool round.
	CompletionSourceModelResponse CompletionSource = "model_response"

	// CompletionSourceDirectToolResults means every call in one model-requested
	// batch targeted a DirectResultTool and returned successfully.
	CompletionSourceDirectToolResults CompletionSource = "direct_tool_results"
)

func (c CompletionSource) Valid() bool {
	return c == CompletionSourceModelResponse || c == CompletionSourceDirectToolResults
}

// Output is the final semantic Interaction result. Response is accumulated
// independently of best-effort stream Delta delivery, so it remains complete
// after observer loss or snapshot restoration.
type Output struct {
	// Source identifies which mutually exclusive result field is authoritative.
	Source CompletionSource `json:"source"`

	// ModelResponse is the authoritative accumulated response when Source is
	// CompletionSourceModelResponse.
	ModelResponse *chat.Response `json:"model_response,omitempty"`

	// DirectToolResults preserves model ToolCall order when Source is
	// CompletionSourceDirectToolResults.
	DirectToolResults []chat.ToolResult `json:"direct_tool_results,omitempty"`

	// ModelCalls is the number of model Effects issued by this Interaction.
	ModelCalls uint32 `json:"model_calls"`
}

func (o Output) Validate() error {
	if !o.Source.Valid() {
		return errors.New("interaction: output source is invalid")
	}
	if o.ModelCalls == 0 {
		return errors.New("interaction: output model_calls must be positive")
	}
	switch o.Source {
	case CompletionSourceModelResponse:
		if o.ModelResponse == nil || len(o.DirectToolResults) != 0 {
			return errors.New("interaction: model_response output requires only ModelResponse")
		}
		if err := o.ModelResponse.Validate(); err != nil {
			return fmt.Errorf("interaction: output model response: %w", err)
		}
		modelOutput := o.ModelResponse.Output
		if modelOutput == nil || modelOutput.Message == nil || modelOutput.FinishReason == "" {
			return errors.New("interaction: output has no finished assistant response")
		}
		for _, part := range modelOutput.Message.Parts {
			if part.ToolCall != nil {
				return errors.New("interaction: final model response contains a pending tool call")
			}
		}
	case CompletionSourceDirectToolResults:
		if o.ModelResponse != nil || len(o.DirectToolResults) == 0 {
			return errors.New("interaction: direct_tool_results output requires only DirectToolResults")
		}
		seen := make(map[string]struct{}, len(o.DirectToolResults))
		for index := range o.DirectToolResults {
			result := o.DirectToolResults[index]
			if err := result.Validate(); err != nil {
				return fmt.Errorf("interaction: direct tool result %d: %w", index, err)
			}
			if result.IsError {
				return fmt.Errorf("interaction: direct tool result %d failed", index)
			}
			if _, duplicate := seen[result.ID]; duplicate {
				return fmt.Errorf("interaction: duplicate direct tool result ID %q", result.ID)
			}
			seen[result.ID] = struct{}{}
		}
	}
	return nil
}

func (o Output) clone() Output {
	cloned := o
	cloned.ModelResponse = o.ModelResponse.Clone()
	cloned.DirectToolResults = cloneToolResults(o.DirectToolResults)
	return cloned
}
