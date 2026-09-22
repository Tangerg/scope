package interaction

import (
	"fmt"

	"github.com/Tangerg/scope/core/chat"
)

const invalidEnumName = "invalid"

// CompletionSource identifies the semantic value that completed an
// Interaction. It is Strategy-owned and does not add a Framework lifecycle
// status.
type CompletionSource string

const (
	CompletionSourceInvalid CompletionSource = ""
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

func (c CompletionSource) String() string {
	if !c.Valid() {
		return invalidEnumName
	}
	return string(c)
}

// Output is the final semantic Interaction result. Response is accumulated
// independently of best-effort stream Delta delivery, so it remains complete
// after observer loss or snapshot restoration.
type Output struct {
	// Source identifies which mutually exclusive result field is authoritative.
	Source CompletionSource `json:"source"`

	// ModelResponse is the authoritative accumulated response when Source is
	// CompletionSourceModelResponse.
	ModelResponse *chat.Response `json:"model_response,omitzero"`

	// DirectToolResults preserves model ToolCall order when Source is
	// CompletionSourceDirectToolResults.
	DirectToolResults []chat.ToolResult `json:"direct_tool_results,omitempty"`

	// ModelCalls is the number of model Effects issued by this Interaction.
	ModelCalls uint64 `json:"model_calls"`
}

func (o Output) Validate() error {
	if !o.Source.Valid() {
		return fmt.Errorf("%w: output source is invalid", ErrInvalidResult)
	}
	if o.ModelCalls == 0 {
		return fmt.Errorf("%w: output model_calls must be positive", ErrInvalidResult)
	}
	switch o.Source {
	case CompletionSourceModelResponse:
		if o.ModelResponse == nil || len(o.DirectToolResults) != 0 {
			return fmt.Errorf("%w: model_response output requires only ModelResponse", ErrInvalidResult)
		}
		if err := o.ModelResponse.Validate(); err != nil {
			return fmt.Errorf("%w: output model response: %w", ErrInvalidResult, err)
		}
		modelOutput := o.ModelResponse.Output
		if modelOutput == nil || modelOutput.Message == nil || modelOutput.FinishReason == "" {
			return fmt.Errorf("%w: output has no finished assistant response", ErrInvalidResult)
		}
		for _, part := range modelOutput.Message.Parts {
			if part.ToolCall != nil {
				return fmt.Errorf("%w: final model response contains a pending tool call", ErrInvalidResult)
			}
		}
	case CompletionSourceDirectToolResults:
		if o.ModelResponse != nil || len(o.DirectToolResults) == 0 {
			return fmt.Errorf("%w: direct_tool_results output requires only DirectToolResults", ErrInvalidResult)
		}
		seen := make(map[string]struct{}, len(o.DirectToolResults))
		for index := range o.DirectToolResults {
			result := o.DirectToolResults[index]
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
	}
	return nil
}

func (o Output) clone() Output {
	cloned := o
	cloned.ModelResponse = o.ModelResponse.Clone()
	cloned.DirectToolResults = cloneToolResults(o.DirectToolResults)
	return cloned
}
