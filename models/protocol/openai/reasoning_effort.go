package openai

import (
	"fmt"
	"slices"

	openaisdk "github.com/openai/openai-go/v3"

	corechat "github.com/Tangerg/scope/core/chat"
)

// openAIReasoningEfforts is OpenAI's reasoning_effort vocabulary. Core's
// effort is the wire word itself, so admitting it is the whole mapping.
var openAIReasoningEfforts = []corechat.ReasoningEffort{
	corechat.ReasoningEffort(openaisdk.ReasoningEffortNone),
	corechat.ReasoningEffort(openaisdk.ReasoningEffortMinimal),
	corechat.ReasoningEffort(openaisdk.ReasoningEffortLow),
	corechat.ReasoningEffort(openaisdk.ReasoningEffortMedium),
	corechat.ReasoningEffort(openaisdk.ReasoningEffortHigh),
	corechat.ReasoningEffort(openaisdk.ReasoningEffortXhigh),
	corechat.ReasoningEffort(openaisdk.ReasoningEffortMax),
}

func validateReasoningEffort(effort corechat.ReasoningEffort, vocabulary []corechat.ReasoningEffort) error {
	if effort != "" && !slices.Contains(vocabulary, effort) {
		return fmt.Errorf("openai: options.reasoning_effort has unsupported value %q", effort)
	}
	return nil
}
